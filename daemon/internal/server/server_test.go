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

// testSocketPath uses /tmp, not t.TempDir(): its /var/folders/... path
// exceeds the ~104-byte macOS Unix socket path cap.
func testSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "zap")
	if err != nil {
		t.Fatalf("mkdir temp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "d.sock")
}

const testDebounce = 25 * time.Millisecond

const stubSuggestion = "stub reply"

func stubSuggest(_ context.Context, req protocol.Request) (protocol.Reply, error) {
	return protocol.Reply{
		V:          protocol.Version,
		ID:         req.ID,
		Source:     protocol.SourceLLM,
		Suggestion: stubSuggestion,
	}, nil
}

func startServer(t *testing.T, path string) (cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	return startServerWithSuggest(t, path, stubSuggest)
}

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

func TestShutdownWithOpenConnection(t *testing.T) {
	path := testSocketPath(t)
	cancel, done := startServer(t, path)

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

	var reply protocol.Reply
	if err := protocol.NewDecoder(conn).Decode(&reply); err != nil {
		t.Fatalf("decode reply: %v", err)
	}
	if reply.ID != "2" {
		t.Errorf("reply.ID = %q, want %q (id of the last request)", reply.ID, "2")
	}
}

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
}

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

	if err := protocol.Encode(conn, protocol.Request{V: protocol.Version, ID: "rec.1", Kind: protocol.KindRecord, Cmd: "ls"}); err != nil {
		t.Fatalf("encode record: %v", err)
	}
	select {
	case <-recorded:
	case <-time.After(2 * time.Second):
		t.Fatal("record fn was not invoked in time")
	}

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
