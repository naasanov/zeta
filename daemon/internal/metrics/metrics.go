// Package metrics implements the dev-only JSONL event log (design §12),
// meant to be stripped in Phase 3 by deleting this package and reverting the
// marked one-liners elsewhere. Nothing outside should depend on internals
// beyond Logger, its constructor, Emit, Close, and the event types.
//
// Three event kinds land in one JSONL file, joined on request_id: "request"
// (built by internal/suggest), and "shown"/"outcome" (sent by the zsh client
// over a second write-only socket, see socket.go).
//
// Emit must never block or add latency: a non-blocking send to a single
// writer goroutine, dropping and counting on a full channel.
package metrics

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
)

// chanBufSize is the writer channel's capacity. Sized generously (design
// §12's "never block" rule) so ordinary bursts never hit the drop path;
// dropping only kicks in under sustained overload, which should never happen
// for a per-keystroke dev log.
const chanBufSize = 1024

// Logger owns the JSONL file and the single writer goroutine that appends to
// it. Construct one with New, call Emit for every event (never blocks), and
// Close it during daemon shutdown to flush and stop the writer cleanly.
type Logger struct {
	user string

	f   *os.File
	mu  sync.Mutex // serializes writes to f (only the writer goroutine writes, but guards Close racing the last write)
	enc *json.Encoder

	ch        chan any
	drops     atomic.Int64
	done      chan struct{} // closed when the writer goroutine returns
	closeOnce sync.Once

	// closeMu guards Emit racing Close: closing ch while a send is in flight
	// panics. Emit takes RLock, Close takes Lock before closing ch.
	closeMu sync.RWMutex
	closed  bool
}

// New opens (creating if needed) the JSONL log at path and starts the
// writer goroutine. user is stamped onto directly-built "request" events;
// passthrough events from the metrics socket are stamped there instead
// (see socket.go).
func New(path, user string) (*Logger, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}

	enc := json.NewEncoder(f)
	// Same reason as protocol.Encode: shell text is full of '<' '>' '&', and
	// we don't want them mangled into \uXXXX escapes in the log.
	enc.SetEscapeHTML(false)

	l := &Logger{
		user: user,
		f:    f,
		enc:  enc,
		ch:   make(chan any, chanBufSize),
		done: make(chan struct{}),
	}
	go l.run()
	return l, nil
}

// User returns the resolved username this Logger stamps onto events. Safe on
// a nil Logger.
func (l *Logger) User() string {
	if l == nil {
		return ""
	}
	return l.user
}

// EmitRequest stamps the resolved user and pending drop count onto ev, then
// emits it. An unstamped event fails silently as an unattributable row.
func (l *Logger) EmitRequest(ev RequestEvent) {
	if l == nil {
		return
	}
	ev.User = l.user
	ev.EventsDroppedSinceLast = l.DropsSinceLast()
	l.Emit(ev)
}

// Emit hands ev to the writer goroutine; never blocks (drops and counts on a
// full channel). Safe no-op on a nil or Close'd Logger.
func (l *Logger) Emit(ev any) {
	if l == nil {
		return
	}
	l.closeMu.RLock()
	defer l.closeMu.RUnlock()
	if l.closed {
		return
	}
	select {
	case l.ch <- ev:
	default:
		l.drops.Add(1)
	}
}

// DropsSinceLast returns the number of events dropped since the last call to
// DropsSinceLast (it reads-and-resets), so the count is observable without
// double-counting across calls.
func (l *Logger) DropsSinceLast() int64 {
	if l == nil {
		return 0
	}
	return l.drops.Swap(0)
}

// run is the single writer goroutine: it drains ch, marshaling and appending
// one JSON line per event, until ch is closed (by Close) and drained.
func (l *Logger) run() {
	defer close(l.done)
	for ev := range l.ch {
		l.mu.Lock()
		if err := l.enc.Encode(ev); err != nil {
			// Best-effort dev log: drop write errors rather than block/panic.
			_ = err
		}
		l.mu.Unlock()
	}
}

// Close stops the writer goroutine and closes the underlying file, draining
// buffered events first. sync.Once makes repeated calls safe; nil Logger is
// a safe no-op.
func (l *Logger) Close() error {
	if l == nil {
		return nil
	}
	var err error
	l.closeOnce.Do(func() {
		l.closeMu.Lock()
		l.closed = true
		l.closeMu.Unlock()

		close(l.ch)
		<-l.done
		l.mu.Lock()
		err = l.f.Close()
		l.mu.Unlock()
	})
	return err
}

// RequestEvent is the "request" event, built and emitted by internal/suggest
// after provider.Complete returns (see the wire contract in the plan doc).
type RequestEvent struct {
	V         int     `json:"v"`
	Event     string  `json:"event"` // always "request"
	TS        float64 `json:"ts"`
	SessionID string  `json:"session_id"`
	RequestID string  `json:"request_id"`
	User      string  `json:"user"`

	Trigger          string  `json:"trigger"` // typing | next_command
	BufferLen        int     `json:"buffer_len"`
	SuggestionLen    int     `json:"suggestion_len"`
	Source           string  `json:"source"`
	TTFTMs           float64 `json:"ttft_ms"`
	SuggestMs        float64 `json:"suggest_ms"`
	InputTokens      int     `json:"input_tokens"`
	OutputTokens     int     `json:"output_tokens"`
	CachedReadTokens int     `json:"cached_read_tokens"`
	HTTPStatus       int     `json:"http_status"`
	StopReason       string  `json:"stop_reason"`

	// CostUSD is a LOWER BOUND, not exact: a superseded call is billed for
	// its prefill but logs zero tokens/cost, since it never got a usage
	// chunk. Filtering cancelled=false drops exactly the unmeasured rows —
	// use this for relative comparisons, not "what did this cost me".
	CostUSD float64 `json:"cost_usd"`

	// PriceTableVersion identifies the price table cost_usd was computed
	// under (see price.go), for telling rows apart after a price correction.
	PriceTableVersion int    `json:"price_table_version"`
	Cancelled         bool   `json:"cancelled"`
	CancelledAtStage  string `json:"cancelled_at_stage"` // "in_flight" | ""

	EventsDroppedSinceLast int64 `json:"events_dropped_since_last"`

	// METRICS(§12): Provider/Model let two adapters serving the same model
	// name at different prices be told apart (see price.go's provider+model
	// key). ErrorType is the unwrapped *provider.Error Kind, empty otherwise.
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	Profile   string `json:"profile"`
	ErrorType string `json:"error_type"`

	// METRICS(§12): opt-in raw-text capture (see EnvRawText/Config.RawText),
	// populated only when enabled; otherwise zero/absent (all omitempty).
	// Buf+Cwd+GitBranch+GitDirty+LastExit+History+HistoryCwds+DirEntries reconstruct
	// the originating protocol.Request verbatim. Suggestion is the FULL
	// reply.Suggestion text and already starts with req.Buf — don't
	// double-prepend Buf when replaying it as a case.
	Buf         string   `json:"buf,omitempty"`
	Suggestion  string   `json:"suggestion,omitempty"`
	Cwd         string   `json:"cwd,omitempty"`
	GitBranch   string   `json:"git_branch,omitempty"`
	GitDirty    bool     `json:"git_dirty,omitempty"`
	LastExit    int      `json:"last_exit,omitempty"`
	History     []string `json:"history,omitempty"`
	HistoryCwds []string `json:"history_cwds,omitempty"` // index-aligned with History; "" = unknown dir
	DirEntries  []string `json:"dir_entries,omitempty"`
}

// SessionID derives the session portion of a request id: everything before
// the last '.' (ids are "<session>.<seq>"). Duplicated from server.shortID's
// split point rather than imported, so this package stays removable.
func SessionID(requestID string) string {
	for i := len(requestID) - 1; i >= 0; i-- {
		if requestID[i] == '.' {
			return requestID[:i]
		}
	}
	return requestID
}
