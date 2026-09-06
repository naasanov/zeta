package server

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/naasanov/zsh-autopilot/daemon/internal/protocol"
	"github.com/naasanov/zsh-autopilot/daemon/internal/provider"
	"github.com/naasanov/zsh-autopilot/daemon/internal/suggest"
)

// testSocketPath returns a short, unique socket path inside a per-test temp
// dir. We root the temp dir at "/tmp" explicitly rather than using t.TempDir():
// on macOS the latter lives under a long /var/folders/... path that, plus a
// filename, exceeds the ~104-byte Unix socket path cap. os.MkdirTemp gives real
// isolation (unique per run, parallel-safe, auto-cleaned) while staying short.
func testSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "zap")
	if err != nil {
		t.Fatalf("mkdir temp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "d.sock")
}

// testDebounce is the debounce duration used by the test helpers below: small
// enough to keep the suite fast, large enough to give a "burst" of rapid
// sends (no sleeps between them) a real window to coalesce in without
// flaking on a loaded CI box.
const testDebounce = 25 * time.Millisecond

// stubSuggestion is what startServer's suggester replies with. Its callers
// check plumbing, not content.
const stubSuggestion = "stub reply"

func stubSuggest(_ context.Context, req protocol.Request) (protocol.Reply, error) {
	return protocol.Reply{
		V:          protocol.Version,
		ID:         req.ID,
		Source:     protocol.SourceLLM,
		Suggestion: stubSuggestion,
	}, nil
}

// startServer runs a Server in the background with a trivial suggester and
// returns a cancel func that shuts it down. It waits for the socket file to
// appear so callers can dial immediately.
func startServer(t *testing.T, path string) (cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	return startServerWithSuggest(t, path, stubSuggest)
}

// startServerWithSuggest is like startServer but installs the caller's suggest
// stub, the seam the coordinator tests use to create deterministic
// cancellation windows. Debounce is set to testDebounce.
func startServerWithSuggest(t *testing.T, path string, suggest func(ctx context.Context, req protocol.Request) (protocol.Reply, error)) (cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(path, log)
	srv.Debounce = testDebounce
	if suggest != nil {
		srv.suggest = suggest
	}

	ctx, cancelFn := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Run(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return cancelFn, errCh
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("server did not create socket %s in time", path)
	return cancelFn, errCh
}

func roundTrip(t *testing.T, path string, req protocol.Request) protocol.Reply {
	t.Helper()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := protocol.Encode(conn, req); err != nil {
		t.Fatalf("encode request: %v", err)
	}

	var reply protocol.Reply
	if err := protocol.NewDecoder(conn).Decode(&reply); err != nil {
		t.Fatalf("decode reply: %v", err)
	}
	return reply
}

func TestTypingRequest(t *testing.T) {
	path := testSocketPath(t)
	cancel, _ := startServer(t, path)
	defer cancel()

	reply := roundTrip(t, path, protocol.Request{
		V:    protocol.Version,
		ID:   "1.1",
		Kind: protocol.KindTyping,
		Buf:  "git",
	})

	if reply.ID != "1.1" {
		t.Errorf("reply.ID = %q, want %q", reply.ID, "1.1")
	}
	if reply.Source != protocol.SourceLLM {
		t.Errorf("reply.Source = %q, want %q", reply.Source, protocol.SourceLLM)
	}
	want := stubSuggestion
	if reply.Suggestion != want {
		t.Errorf("reply.Suggestion = %q, want %q", reply.Suggestion, want)
	}
}

func TestEmptyBufferRequest(t *testing.T) {
	path := testSocketPath(t)
	cancel, _ := startServer(t, path)
	defer cancel()

	reply := roundTrip(t, path, protocol.Request{
		V:    protocol.Version,
		ID:   "2.1",
		Kind: protocol.KindNextCommand,
		Buf:  "",
	})

	if reply.ID != "2.1" {
		t.Errorf("reply.ID = %q, want %q", reply.ID, "2.1")
	}
	want := stubSuggestion
	if reply.Suggestion != want {
		t.Errorf("reply.Suggestion = %q, want %q", reply.Suggestion, want)
	}
}

// TestShutdownWithOpenConnection guards against a shutdown deadlock: the zsh
// client holds a persistent warm connection, so at shutdown a handler is
// blocked in Decode. Run must close in-flight connections before waiting for
// their handlers, or it hangs forever.
func TestShutdownWithOpenConnection(t *testing.T) {
	path := testSocketPath(t)
	cancel, done := startServer(t, path)

	// Dial and keep the connection open without sending a request, mirroring
	// the client's warm socket parked in Decode.
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error on shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not shut down with an open connection (deadlock)")
	}
}

func TestShutdownRemovesSocket(t *testing.T) {
	path := testSocketPath(t)
	cancel, done := startServer(t, path)

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error on shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not shut down in time")
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("socket file still exists after shutdown: err=%v", err)
	}
}

// TestSupersedeCancelsInFlightRequest dispatches A (blocks until cancelled),
// then B on the same connection, and asserts A's ctx is cancelled and only
// B's reply is written (A must observe ctx.Err() and skip its write).
func TestSupersedeCancelsInFlightRequest(t *testing.T) {
	path := testSocketPath(t)

	started := make(chan string, 2)
	cancelled := make(chan string, 2)

	suggest := func(ctx context.Context, req protocol.Request) (protocol.Reply, error) {
		if req.ID == "A" {
			started <- req.ID
			<-ctx.Done()
			cancelled <- req.ID
			return protocol.Reply{}, ctx.Err()
		}
		return protocol.Reply{
			V:          protocol.Version,
			ID:         req.ID,
			Source:     protocol.SourceLLM,
			Suggestion: "b-reply",
		}, nil
	}

	cancel, _ := startServerWithSuggest(t, path, suggest)
	defer cancel()

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	dec := protocol.NewDecoder(conn)

	if err := protocol.Encode(conn, protocol.Request{V: protocol.Version, ID: "A", Kind: protocol.KindTyping, Buf: "gi"}); err != nil {
		t.Fatalf("encode A: %v", err)
	}

	select {
	case id := <-started:
		if id != "A" {
			t.Fatalf("started id = %q, want A", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("request A did not start in time")
	}

	if err := protocol.Encode(conn, protocol.Request{V: protocol.Version, ID: "B", Kind: protocol.KindTyping, Buf: "git"}); err != nil {
		t.Fatalf("encode B: %v", err)
	}

	select {
	case id := <-cancelled:
		if id != "A" {
			t.Fatalf("cancelled id = %q, want A", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("request A was not cancelled by superseding request B")
	}

	var reply protocol.Reply
	if err := dec.Decode(&reply); err != nil {
		t.Fatalf("decode reply: %v", err)
	}
	if reply.ID != "B" {
		t.Fatalf("reply.ID = %q, want B (A must not write after being cancelled)", reply.ID)
	}
	if reply.Suggestion != "b-reply" {
		t.Errorf("reply.Suggestion = %q, want %q", reply.Suggestion, "b-reply")
	}
}

// TestCancelOnConnectionClose: with no superseding request, closing the
// client connection must still cancel whatever is in flight.
func TestCancelOnConnectionClose(t *testing.T) {
	path := testSocketPath(t)

	started := make(chan struct{})
	cancelled := make(chan struct{})

	suggest := func(ctx context.Context, req protocol.Request) (protocol.Reply, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return protocol.Reply{}, ctx.Err()
	}

	cancel, _ := startServerWithSuggest(t, path, suggest)
	defer cancel()

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	if err := protocol.Encode(conn, protocol.Request{V: protocol.Version, ID: "1", Kind: protocol.KindTyping, Buf: "x"}); err != nil {
		t.Fatalf("encode: %v", err)
	}

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("request did not start in time")
	}

	if err := conn.Close(); err != nil {
		t.Fatalf("close conn: %v", err)
	}

	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight request was not cancelled when connection closed")
	}
}

// TestDebounceCoalescesBurst: a burst faster than the debounce window must
// produce exactly ONE call into suggest, for the LAST buffered request —
// every superseded buffer must never even be sent, not merely cancelled
// after being sent.
func TestDebounceCoalescesBurst(t *testing.T) {
	path := testSocketPath(t)

	var mu sync.Mutex
	var calls []string
	suggest := func(_ context.Context, req protocol.Request) (protocol.Reply, error) {
		mu.Lock()
		calls = append(calls, req.Buf)
		mu.Unlock()
		return protocol.Reply{V: protocol.Version, ID: req.ID, Source: protocol.SourceLLM, Suggestion: req.Buf}, nil
	}

	cancel, _ := startServerWithSuggest(t, path, suggest)
	defer cancel()

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	for i, buf := range []string{"g", "gi", "git"} {
		if err := protocol.Encode(conn, protocol.Request{V: protocol.Version, ID: fmt.Sprintf("%d", i), Kind: protocol.KindTyping, Buf: buf}); err != nil {
			t.Fatalf("encode %q: %v", buf, err)
		}
	}

	// Wait comfortably past the debounce window for dispatch to happen.
	time.Sleep(testDebounce * 4)

	mu.Lock()
	got := append([]string(nil), calls...)
	mu.Unlock()

	if len(got) != 1 {
		t.Fatalf("suggest called %d times, want exactly 1; calls=%v", len(got), got)
	}
	if got[0] != "git" {
		t.Errorf("suggest called with buf %q, want %q (the last buffered request)", got[0], "git")
	}

	// The reply for the dispatched (last) request should be waiting on the
	// wire.
	var reply protocol.Reply
	if err := protocol.NewDecoder(conn).Decode(&reply); err != nil {
		t.Fatalf("decode reply: %v", err)
	}
	if reply.ID != "2" {
		t.Errorf("reply.ID = %q, want %q (id of the last request)", reply.ID, "2")
	}
}

// TestDebounceFiresAfterQuiet: a single request with nothing superseding it
// must still get answered after the quiet period.
func TestDebounceFiresAfterQuiet(t *testing.T) {
	path := testSocketPath(t)
	cancel, _ := startServer(t, path)
	defer cancel()

	start := time.Now()
	reply := roundTrip(t, path, protocol.Request{V: protocol.Version, ID: "1", Kind: protocol.KindTyping, Buf: "git"})
	elapsed := time.Since(start)

	if reply.ID != "1" {
		t.Errorf("reply.ID = %q, want %q", reply.ID, "1")
	}
	if elapsed < testDebounce {
		t.Errorf("reply arrived after %v, want at least the debounce window (%v)", elapsed, testDebounce)
	}
}

// TestSupersedeAfterDebounceStillWorks: once a request has cleared debounce
// and is in flight, a new request (after its own debounce window) must still
// cancel it.
func TestSupersedeAfterDebounceStillWorks(t *testing.T) {
	path := testSocketPath(t)

	started := make(chan string, 2)
	cancelled := make(chan string, 2)

	suggest := func(ctx context.Context, req protocol.Request) (protocol.Reply, error) {
		if req.ID == "A" {
			started <- req.ID
			<-ctx.Done()
			cancelled <- req.ID
			return protocol.Reply{}, ctx.Err()
		}
		return protocol.Reply{V: protocol.Version, ID: req.ID, Source: protocol.SourceLLM, Suggestion: "b-reply"}, nil
	}

	cancel, _ := startServerWithSuggest(t, path, suggest)
	defer cancel()

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	dec := protocol.NewDecoder(conn)

	if err := protocol.Encode(conn, protocol.Request{V: protocol.Version, ID: "A", Kind: protocol.KindTyping, Buf: "gi"}); err != nil {
		t.Fatalf("encode A: %v", err)
	}

	// Let A clear debounce and actually dispatch (block in suggest).
	select {
	case id := <-started:
		if id != "A" {
			t.Fatalf("started id = %q, want A", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("request A did not start in time")
	}

	// Now send B; it must go through its own debounce window before
	// dispatch, at which point it supersedes A.
	if err := protocol.Encode(conn, protocol.Request{V: protocol.Version, ID: "B", Kind: protocol.KindTyping, Buf: "git"}); err != nil {
		t.Fatalf("encode B: %v", err)
	}

	select {
	case id := <-cancelled:
		if id != "A" {
			t.Fatalf("cancelled id = %q, want A", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("request A was not cancelled by superseding request B")
	}

	var reply protocol.Reply
	if err := dec.Decode(&reply); err != nil {
		t.Fatalf("decode reply: %v", err)
	}
	if reply.ID != "B" {
		t.Fatalf("reply.ID = %q, want B (A must not write after being cancelled)", reply.ID)
	}
}

// TestNoGoroutineLeak drives many rapidly-superseding requests across several
// connections, tears everything down, and asserts runtime.NumGoroutine()
// settles back near baseline — a coordinator that fails to cancel (or a
// goroutine stuck on something other than ctx) leaves the count elevated.
func TestNoGoroutineLeak(t *testing.T) {
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	path := testSocketPath(t)

	suggest := func(ctx context.Context, req protocol.Request) (protocol.Reply, error) {
		select {
		case <-ctx.Done():
			return protocol.Reply{}, ctx.Err()
		case <-time.After(30 * time.Millisecond):
			return protocol.Reply{
				V:          protocol.Version,
				ID:         req.ID,
				Source:     protocol.SourceLLM,
				Suggestion: "x",
			}, nil
		}
	}

	cancel, _ := startServerWithSuggest(t, path, suggest)

	const nConns = 5
	const nReqsPerConn = 50
	var wg sync.WaitGroup
	for i := range nConns {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			conn, err := net.Dial("unix", path)
			if err != nil {
				t.Errorf("dial: %v", err)
				return
			}
			defer conn.Close()
			for j := range nReqsPerConn {
				req := protocol.Request{
					V:    protocol.Version,
					ID:   fmt.Sprintf("%d.%d", i, j),
					Kind: protocol.KindTyping,
					Buf:  "git",
				}
				if err := protocol.Encode(conn, req); err != nil {
					return
				}
			}
		}(i)
	}
	wg.Wait()

	cancel()

	const slack = 5
	deadline := time.Now().Add(5 * time.Second)
	for {
		runtime.GC()
		cur := runtime.NumGoroutine()
		if cur <= baseline+slack {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutine leak: baseline=%d, still at %d after shutdown+settle", baseline, cur)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestRecordRequestInvokesRecordFn asserts a KindRecord request is delivered
// to the registered record func with its Cmd/Cwd intact.
func TestRecordRequestInvokesRecordFn(t *testing.T) {
	path := testSocketPath(t)

	recorded := make(chan protocol.Request, 1)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(path, log)
	srv.Debounce = testDebounce
	srv.SetRecord(func(req protocol.Request) {
		recorded <- req
	})

	ctx, cancelFn := context.WithCancel(context.Background())
	defer cancelFn()
	go func() { srv.Run(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := protocol.Encode(conn, protocol.Request{
		V:    protocol.Version,
		ID:   "1.1",
		Kind: protocol.KindRecord,
		Cwd:  "/home/u/p",
		Cmd:  "git status",
	}); err != nil {
		t.Fatalf("encode: %v", err)
	}

	select {
	case req := <-recorded:
		if req.Cmd != "git status" {
			t.Errorf("req.Cmd = %q, want %q", req.Cmd, "git status")
		}
		if req.Cwd != "/home/u/p" {
			t.Errorf("req.Cwd = %q, want %q", req.Cwd, "/home/u/p")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("record fn was not invoked in time")
	}
}

// TestRecordRequestProducesNoReplyAndSkipsSuggest asserts a KindRecord
// request never reaches suggest and never gets a Reply on the wire: only the
// typing request sent afterward should produce one.
func TestRecordRequestProducesNoReplyAndSkipsSuggest(t *testing.T) {
	path := testSocketPath(t)

	var mu sync.Mutex
	var suggestCalls int
	suggest := func(_ context.Context, req protocol.Request) (protocol.Reply, error) {
		mu.Lock()
		suggestCalls++
		mu.Unlock()
		return protocol.Reply{V: protocol.Version, ID: req.ID, Source: protocol.SourceLLM, Suggestion: "reply"}, nil
	}

	recorded := make(chan protocol.Request, 1)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(path, log)
	srv.Debounce = testDebounce
	srv.suggest = suggest
	srv.SetRecord(func(req protocol.Request) { recorded <- req })

	ctx, cancelFn := context.WithCancel(context.Background())
	defer cancelFn()
	go func() { srv.Run(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := protocol.Encode(conn, protocol.Request{
		V: protocol.Version, ID: "1.1", Kind: protocol.KindRecord, Cmd: "ls",
	}); err != nil {
		t.Fatalf("encode record: %v", err)
	}

	select {
	case <-recorded:
	case <-time.After(2 * time.Second):
		t.Fatal("record fn was not invoked in time")
	}

	// Nothing should have been written to the wire for the record request.
	// A subsequent typing request is used to prove the connection is still
	// alive and that its reply is the only one that arrives.
	if err := protocol.Encode(conn, protocol.Request{
		V: protocol.Version, ID: "1.2", Kind: protocol.KindTyping, Buf: "git",
	}); err != nil {
		t.Fatalf("encode typing: %v", err)
	}

	var reply protocol.Reply
	if err := protocol.NewDecoder(conn).Decode(&reply); err != nil {
		t.Fatalf("decode reply: %v", err)
	}
	if reply.ID != "1.2" {
		t.Errorf("reply.ID = %q, want %q (record request must not reply)", reply.ID, "1.2")
	}

	mu.Lock()
	got := suggestCalls
	mu.Unlock()
	if got != 1 {
		t.Errorf("suggest called %d times, want exactly 1 (record request must never reach suggest)", got)
	}
}

// TestRecordBetweenTypingRequestsDoesNotSupersede sends typing(A), record,
// typing(B) back to back and asserts the record neither cancels A nor
// resets B's debounce window: A must still complete and only B's reply
// (the winner of normal supersede) reaches the wire.
func TestRecordBetweenTypingRequestsDoesNotSupersede(t *testing.T) {
	path := testSocketPath(t)

	started := make(chan string, 2)
	cancelled := make(chan string, 2)
	recorded := make(chan protocol.Request, 1)

	suggest := func(ctx context.Context, req protocol.Request) (protocol.Reply, error) {
		if req.ID == "A" {
			started <- req.ID
			<-ctx.Done()
			cancelled <- req.ID
			return protocol.Reply{}, ctx.Err()
		}
		return protocol.Reply{V: protocol.Version, ID: req.ID, Source: protocol.SourceLLM, Suggestion: "b-reply"}, nil
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(path, log)
	srv.Debounce = testDebounce
	srv.suggest = suggest
	srv.SetRecord(func(req protocol.Request) { recorded <- req })

	ctx, cancelFn := context.WithCancel(context.Background())
	defer cancelFn()
	go func() { srv.Run(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	dec := protocol.NewDecoder(conn)

	if err := protocol.Encode(conn, protocol.Request{V: protocol.Version, ID: "A", Kind: protocol.KindTyping, Buf: "gi"}); err != nil {
		t.Fatalf("encode A: %v", err)
	}

	select {
	case id := <-started:
		if id != "A" {
			t.Fatalf("started id = %q, want A", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("request A did not start in time")
	}

	// A record request while A is in flight must not touch supersede state.
	if err := protocol.Encode(conn, protocol.Request{V: protocol.Version, ID: "rec.1", Kind: protocol.KindRecord, Cmd: "ls"}); err != nil {
		t.Fatalf("encode record: %v", err)
	}
	select {
	case <-recorded:
	case <-time.After(2 * time.Second):
		t.Fatal("record fn was not invoked in time")
	}

	// A must still be in flight (not cancelled by the record).
	select {
	case <-cancelled:
		t.Fatal("request A was cancelled by an intervening record request")
	case <-time.After(testDebounce * 2):
	}

	if err := protocol.Encode(conn, protocol.Request{V: protocol.Version, ID: "B", Kind: protocol.KindTyping, Buf: "git"}); err != nil {
		t.Fatalf("encode B: %v", err)
	}

	select {
	case id := <-cancelled:
		if id != "A" {
			t.Fatalf("cancelled id = %q, want A", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("request A was not cancelled by superseding request B")
	}

	var reply protocol.Reply
	if err := dec.Decode(&reply); err != nil {
		t.Fatalf("decode reply: %v", err)
	}
	if reply.ID != "B" {
		t.Fatalf("reply.ID = %q, want B", reply.ID)
	}
}

// TestNoticeOnNonRecoverableError asserts a suggest error that NoticeFor
// classifies as surfaceable (auth) produces exactly one Reply with the
// notice fields set and an empty Suggestion, rather than nothing at all.
func TestNoticeOnNonRecoverableError(t *testing.T) {
	path := testSocketPath(t)

	suggestFn := func(context.Context, protocol.Request) (protocol.Reply, error) {
		return protocol.Reply{}, &provider.Error{Kind: provider.ErrAuth, HTTPStatus: 401, Provider: "codestral"}
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(path, log)
	srv.Debounce = testDebounce
	srv.suggest = suggestFn
	srv.SetNotice(suggest.NoticeFor("codestral", "ZSH_AUTOPILOT_CODESTRAL_KEY"))

	ctx, cancelFn := context.WithCancel(context.Background())
	defer cancelFn()
	go func() { srv.Run(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	reply := roundTrip(t, path, protocol.Request{V: protocol.Version, ID: "1", Kind: protocol.KindTyping, Buf: "git"})

	if reply.NoticeKind != "auth" {
		t.Errorf("reply.NoticeKind = %q, want %q", reply.NoticeKind, "auth")
	}
	if reply.Notice == "" {
		t.Error("reply.Notice is empty, want a diagnostic message")
	}
	if reply.Suggestion != "" {
		t.Errorf("reply.Suggestion = %q, want empty", reply.Suggestion)
	}
	if reply.ID != "1" {
		t.Errorf("reply.ID = %q, want %q", reply.ID, "1")
	}
}

// TestNoRecoverableErrorWritesNothing asserts a suggest error NoticeFor does
// not surface (rate limited, recoverable by design) produces no write at all
// on the wire.
func TestNoRecoverableErrorWritesNothing(t *testing.T) {
	path := testSocketPath(t)

	suggestFn := func(context.Context, protocol.Request) (protocol.Reply, error) {
		return protocol.Reply{}, &provider.Error{Kind: provider.ErrRateLimited, HTTPStatus: 429, Provider: "groq"}
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(path, log)
	srv.Debounce = testDebounce
	srv.suggest = suggestFn
	srv.SetNotice(suggest.NoticeFor("groq", "ZSH_AUTOPILOT_GROQ_KEY"))

	ctx, cancelFn := context.WithCancel(context.Background())
	defer cancelFn()
	go func() { srv.Run(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := protocol.Encode(conn, protocol.Request{V: protocol.Version, ID: "1", Kind: protocol.KindTyping, Buf: "git"}); err != nil {
		t.Fatalf("encode: %v", err)
	}

	// Give the debounce + dispatch + would-be write time to happen, then
	// assert nothing showed up: a short read deadline turns "no bytes ever
	// arrive" into a fast, deterministic timeout instead of hanging.
	conn.SetReadDeadline(time.Now().Add(testDebounce*4 + 200*time.Millisecond))
	var reply protocol.Reply
	err = protocol.NewDecoder(conn).Decode(&reply)
	if err == nil {
		t.Fatalf("expected no reply for a non-surfaceable error, got %+v", reply)
	}
	if ne, ok := err.(net.Error); !ok || !ne.Timeout() {
		t.Fatalf("expected a read timeout, got: %v", err)
	}
}
