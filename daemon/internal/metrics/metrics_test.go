package metrics

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readLines reads and returns all lines currently in path.
func readLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan %s: %v", path, err)
	}
	return lines
}

func TestLogger_EmitWritesLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "events.jsonl")

	l, err := New(path, "alice")
	if err != nil {
		t.Fatalf("New() err = %v", err)
	}

	ev := RequestEvent{V: 1, Event: "request", RequestID: "sess.1", User: "alice"}
	l.Emit(ev)

	if err := l.Close(); err != nil {
		t.Fatalf("Close() err = %v", err)
	}

	lines := readLines(t, path)
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1: %v", len(lines), lines)
	}

	var decoded map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &decoded); err != nil {
		t.Fatalf("unmarshal line: %v", err)
	}
	if decoded["event"] != "request" {
		t.Errorf("event = %v, want %q", decoded["event"], "request")
	}
	if decoded["request_id"] != "sess.1" {
		t.Errorf("request_id = %v, want %q", decoded["request_id"], "sess.1")
	}
}

// TestLogger_EmitRequestStampsUser: EmitRequest must stamp User even when
// the caller builds RequestEvent without one (internal/suggest has no
// Logger to ask). Unstamped events fail silently as unattributable rows.
func TestLogger_EmitRequestStampsUser(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")

	l, err := New(path, "alice")
	if err != nil {
		t.Fatalf("New() err = %v", err)
	}

	l.EmitRequest(RequestEvent{V: 1, Event: "request", RequestID: "sess.1"})

	if err := l.Close(); err != nil {
		t.Fatalf("Close() err = %v", err)
	}

	lines := readLines(t, path)
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1: %v", len(lines), lines)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &decoded); err != nil {
		t.Fatalf("unmarshal line: %v", err)
	}
	if decoded["user"] != "alice" {
		t.Errorf("user = %v, want %q (request events must carry the resolved user)", decoded["user"], "alice")
	}
}

// TestLogger_NoHTMLEscaping asserts '<', '>', '&' survive verbatim in the
// written line — the same reason protocol.Encode disables HTML escaping:
// shell text is full of these characters.
func TestLogger_NoHTMLEscaping(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")

	l, err := New(path, "alice")
	if err != nil {
		t.Fatalf("New() err = %v", err)
	}

	l.Emit(map[string]any{"v": 1, "event": "outcome", "note": "a < b && c > d"})
	if err := l.Close(); err != nil {
		t.Fatalf("Close() err = %v", err)
	}

	lines := readLines(t, path)
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1", len(lines))
	}
	if !strings.Contains(lines[0], "a < b && c > d") {
		t.Errorf("line = %q, want literal %q substring (no \\u003c-style HTML escaping)", lines[0], "a < b && c > d")
	}
}

// TestLogger_DropOnFull asserts Emit never blocks: filling the channel past
// capacity drops events and the drop counter observes exactly how many.
func TestLogger_DropOnFull(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")

	l, err := New(path, "alice")
	if err != nil {
		t.Fatalf("New() err = %v", err)
	}
	defer l.Close()

	// Hold the writer's mutex so the channel actually fills.
	l.mu.Lock()

	total := chanBufSize + 50
	for i := 0; i < total; i++ {
		l.Emit(map[string]any{"i": i})
	}

	l.mu.Unlock()

	drops := l.DropsSinceLast()
	if drops <= 0 {
		t.Errorf("DropsSinceLast() = %d, want > 0 (channel of size %d should have overflowed with %d emits)", drops, chanBufSize, total)
	}

	// Reading again immediately should be 0 (read-and-reset semantics).
	if again := l.DropsSinceLast(); again != 0 {
		t.Errorf("second DropsSinceLast() = %d, want 0 (drops should reset after read)", again)
	}
}

// TestLogger_CloseNoLeakOrPanic asserts Close stops the writer goroutine
// cleanly and that a concurrent Emit racing Close does not panic (Emit must
// lose gracefully, not send-on-closed-channel).
func TestLogger_CloseNoLeakOrPanic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")

	l, err := New(path, "alice")
	if err != nil {
		t.Fatalf("New() err = %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			l.Emit(map[string]any{"i": i})
		}
	}()

	// Close races the still-running Emit loop on purpose.
	if err := l.Close(); err != nil {
		t.Fatalf("Close() err = %v", err)
	}
	<-done

	// Second Close must also be safe (sync.Once).
	if err := l.Close(); err != nil {
		t.Fatalf("second Close() err = %v", err)
	}
}

func TestLogger_NilIsSafeNoOp(t *testing.T) {
	var l *Logger
	l.Emit(map[string]any{"x": 1}) // must not panic
	if got := l.DropsSinceLast(); got != 0 {
		t.Errorf("nil Logger DropsSinceLast() = %d, want 0", got)
	}
	if err := l.Close(); err != nil {
		t.Errorf("nil Logger Close() err = %v, want nil", err)
	}
}

func TestSessionID(t *testing.T) {
	cases := map[string]string{
		"sess.1":        "sess",
		"a.b.c.42":      "a.b.c",
		"noseparator":   "noseparator",
		"":              "",
		"trailing.dot.": "trailing.dot",
	}
	for in, want := range cases {
		if got := SessionID(in); got != want {
			t.Errorf("SessionID(%q) = %q, want %q", in, got, want)
		}
	}
}
