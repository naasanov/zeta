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

// startServer runs a Server in the background and returns it along with a
// cancel func that shuts it down. It waits for the socket file to appear
// before returning so callers can dial immediately.
func startServer(t *testing.T, path string) (cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	return startServerWithSuggest(t, path, nil)
}

// startServerWithSuggest is like startServer but lets the caller install a
// controlled suggest stub (overriding the default instant-echo one) before
// the server starts accepting connections. Passing nil keeps the default.
// This is the seam the coordinator tests use to create deterministic
// cancellation windows: a stub that blocks on ctx (or a per-request channel)
// instead of an LLM call.
func startServerWithSuggest(t *testing.T, path string, suggest func(ctx context.Context, req protocol.Request) (protocol.Reply, error)) (cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(path, log)
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
	want := "git --oneline"
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
	want := "git status"
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

// TestSupersedeCancelsInFlightRequest forces the coordinator's core race: it
// dispatches request "A" whose stub suggester blocks on <-ctx.Done() (so A
// only completes once cancelled), waits for A to actually start, then
// dispatches "B" on the same connection. If the coordinator's supersede logic
// (currentRequest.cancel invoked before installing the new slot) is missing
// or wrong, A's ctx is never cancelled and this test times out waiting on the
// cancelled channel. Then it asserts the reply that comes back is B's, not
// A's (A observes ctx.Err() and skips its write, per the "cancelled requests
// don't write" invariant).
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

// TestCancelOnConnectionClose forces the other half of the coordinator: with
// no superseding request, closing the client connection must still cancel
// whatever is in flight. The stub blocks on ctx and signals a channel once
// unblocked; if handle's teardown didn't call connCancel() before returning,
// this request's context would never be cancelled and the test times out.
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

// TestNoGoroutineLeak drives many rapidly-superseding requests (no reply
// read, no delay between sends, so almost every request is superseded before
// its stub's artificial 30ms "work" completes) across several concurrent
// connections, closes them, shuts the server down, and asserts
// runtime.NumGoroutine() settles back near its pre-test baseline. Every
// superseded/torn-down request's stub returns promptly via its ctx.Done()
// case, so if the coordinator ever failed to cancel (or a goroutine got
// stuck waiting on something that isn't ctx), the count would stay elevated
// and the poll loop below would time out and fail.
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
