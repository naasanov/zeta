package server

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
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
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(path, log)

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
