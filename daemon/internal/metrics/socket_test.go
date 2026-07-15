package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// shortSocketPath returns a socket path under /tmp short enough to survive
// macOS's ~104-byte unix socket path cap; t.TempDir() paths are frequently
// too long for this.
func shortSocketPath(t *testing.T) string {
	t.Helper()
	p := filepath.Join(os.TempDir(), fmt.Sprintf("apm-%d.sock", time.Now().UnixNano()))
	t.Cleanup(func() { os.Remove(p) })
	return p
}

// TestServe_PassthroughStampsUserAndSession drives one connection through
// the metrics socket, sends a "shown" event, and asserts the written line in
// the log has been stamped with user + a derived session_id while every
// original field survives untouched (design: passthrough, don't drop
// unknown/additive fields).
func TestServe_PassthroughStampsUserAndSession(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	sockPath := shortSocketPath(t)

	l, err := New(logPath, "alice")
	if err != nil {
		t.Fatalf("New() err = %v", err)
	}
	defer l.Close()

	ctx, cancel := context.WithCancel(context.Background())
	serveErrCh := make(chan error, 1)
	go func() { serveErrCh <- Serve(ctx, sockPath, l, slog.Default()) }()

	// Wait for the socket to appear (Serve claims/binds it asynchronously
	// relative to this test goroutine).
	deadline := time.Now().Add(2 * time.Second)
	var conn net.Conn
	for time.Now().Before(deadline) {
		conn, err = net.Dial("unix", sockPath)
		if err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if conn == nil {
		t.Fatalf("dial %s: %v", sockPath, err)
	}
	defer conn.Close()

	// Mirrors the real zsh-side "shown" payload exactly (zsh/55_metrics.zsh).
	// Deliberately no buffer_len: that field is unobtainable at paint time (the
	// zsh emit runs in a `zle -F` callback with no $BUFFER) and lives on the
	// daemon-built "request" event instead, joined via request_id.
	line := `{"v":1,"event":"shown","request_id":"sess-xyz.3","total_latency_ms":123.4,"suggestion_len":4,"ts":1750000000.123}` + "\n"
	if _, err := conn.Write([]byte(line)); err != nil {
		t.Fatalf("write: %v", err)
	}
	conn.Close()

	// Give the writer goroutine a moment to land the line, then tear down.
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-serveErrCh:
		if err != nil {
			t.Fatalf("Serve() err = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve() did not return after ctx cancellation (teardown hung)")
	}

	if err := l.Close(); err != nil {
		t.Fatalf("Close() err = %v", err)
	}

	lines := readLines(t, logPath)
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1: %v", len(lines), lines)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded["user"] != "alice" {
		t.Errorf("user = %v, want %q", decoded["user"], "alice")
	}
	if decoded["session_id"] != "sess-xyz" {
		t.Errorf("session_id = %v, want %q", decoded["session_id"], "sess-xyz")
	}
	if decoded["event"] != "shown" {
		t.Errorf("event = %v, want %q", decoded["event"], "shown")
	}
	if decoded["request_id"] != "sess-xyz.3" {
		t.Errorf("request_id = %v, want %q", decoded["request_id"], "sess-xyz.3")
	}
	// Additive field survives untouched: passthrough must not drop it.
	if decoded["total_latency_ms"] != 123.4 {
		t.Errorf("total_latency_ms = %v, want 123.4", decoded["total_latency_ms"])
	}
}

// TestServe_ShutdownDoesNotHang asserts Serve tears down promptly (no
// deadlock) when ctx is cancelled while a connection is open but idle,
// mirroring internal/server's own close-conns-then-wait discipline.
func TestServe_ShutdownDoesNotHang(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	sockPath := shortSocketPath(t)

	l, err := New(logPath, "bob")
	if err != nil {
		t.Fatalf("New() err = %v", err)
	}
	defer l.Close()

	ctx, cancel := context.WithCancel(context.Background())
	serveErrCh := make(chan error, 1)
	go func() { serveErrCh <- Serve(ctx, sockPath, l, slog.Default()) }()

	deadline := time.Now().Add(2 * time.Second)
	var conn net.Conn
	for time.Now().Before(deadline) {
		conn, err = net.Dial("unix", sockPath)
		if err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if conn == nil {
		t.Fatalf("dial %s: %v", sockPath, err)
	}
	defer conn.Close()
	// Deliberately leave the connection open and idle (no write, no close)
	// so the reader goroutine inside Serve is parked in a blocking read when
	// shutdown starts.

	cancel()
	select {
	case err := <-serveErrCh:
		if err != nil {
			t.Fatalf("Serve() err = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve() did not return after ctx cancellation with an idle open conn (teardown hung)")
	}
}
