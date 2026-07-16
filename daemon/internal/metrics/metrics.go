// Package metrics implements the dev-only JSONL event log (design §12). It is
// dogfooding-only and is meant to be stripped in Phase 3 by deleting this
// whole package and reverting the marked one-liners in the rest of the
// daemon. Nothing outside this package should depend on internals beyond
// Logger, its constructor, Emit, Close, and the event types.
//
// Three event kinds land in one JSONL file, joined later on request_id:
//   - "request", built and emitted entirely inside internal/suggest (this
//     package only defines the shape and does the writing).
//   - "shown" and "outcome", sent by the zsh client over a second, write-only
//     Unix socket (see socket.go) and passed through close to verbatim.
//
// Hot-path rule (§12): Emit must never block or add latency to a request.
// It's a non-blocking channel send to a single writer goroutine; a full
// channel drops the event and bumps an atomic counter instead of blocking.
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

	// closeMu + closed guard the Emit-races-Close hazard: closing ch while
	// another goroutine is mid-send on it panics, so Close must not close ch
	// until it holds the write lock (i.e. no Emit is between its RLock and
	// the send). Emit takes the read side, so many Emits can proceed
	// concurrently; Close takes the write side once, exclusively, before
	// closing ch.
	closeMu sync.RWMutex
	closed  bool
}

// New opens (creating if needed) the JSONL log at path, creating parent
// directories as needed, and starts the single writer goroutine. user is
// stamped onto every event this Logger's callers build directly (the
// "request" event); passthrough events from the metrics socket are stamped
// there instead (see socket.go), since a Logger may serve multiple users'
// connections in principle.
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

// User returns the resolved username this Logger stamps onto events.
// Passthrough events (shown/outcome) are stamped by socket.go; directly-built
// "request" events are stamped by EmitRequest. Safe on a nil Logger.
func (l *Logger) User() string {
	if l == nil {
		return ""
	}
	return l.user
}

// EmitRequest stamps the resolved user and the pending drop count onto ev,
// then emits it. The stamping lives here rather than at the wiring site so the
// "every event carries a user" invariant is testable: per-user attribution is
// the whole point of shipping this log to other machines, and an unstamped
// event fails silently — it produces a well-formed line with an empty user
// that only shows up as unattributable rows at analysis time.
func (l *Logger) EmitRequest(ev RequestEvent) {
	if l == nil {
		return
	}
	ev.User = l.user
	ev.EventsDroppedSinceLast = l.DropsSinceLast()
	l.Emit(ev)
}

// Emit hands ev (any JSON-marshalable value, typically a RequestEvent or a
// map[string]any passthrough) to the writer goroutine. It never blocks: if
// the writer is behind and the channel is full, the event is dropped and the
// drop counter is incremented instead of stalling the caller (design §12
// "metrics never add latency"). Emit on a nil Logger, or on one that has
// already been Close'd, is a safe no-op — call sites don't need a nil check
// when metrics are disabled, and Emit racing Close (e.g. a metrics socket
// connection still draining as shutdown begins) cannot panic on a
// send-on-closed-channel.
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
			// Best-effort dev log; nowhere sensible to report a write error
			// from a background goroutine. Drop it rather than block/panic.
			_ = err
		}
		l.mu.Unlock()
	}
}

// Close stops the writer goroutine and closes the underlying file. It first
// takes closeMu's write side and sets closed=true — which blocks until every
// Emit currently mid-send has returned, and causes every subsequent Emit to
// no-op instead of sending — so it is then safe to close(ch) without any
// goroutine racing a send against it (no send-on-closed-channel panic).
// Closing ch causes run's range loop to drain remaining buffered events and
// exit; Close waits for that (<-l.done) before closing the file, so no
// buffered event is lost and no write races the close. sync.Once makes
// repeated Close calls safe. Close on a nil Logger is a safe no-op.
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

	// CostUSD is a LOWER BOUND on real spend — do not quote it as exact.
	//
	// Token counts only exist for calls that returned a usage chunk. A
	// superseded call (ctx cancelled mid-flight when a newer keystroke
	// arrives) never gets one, so it logs zero tokens and zero cost — but the
	// request was already sent and the provider almost certainly billed the
	// prefill it had done. Roughly half of all calls are superseded in
	// practice (~47% in the first real dogfooding session), so the shortfall
	// is large, not a rounding error.
	//
	// Filtering `cancelled=false` (the natural way to sum this column) is
	// exactly what hides the problem: it drops the very rows whose cost is
	// unmeasured. Sum it for relative comparisons between models/configs, not
	// for "what did this cost me".
	//
	// The same wasted work drives 429s: cancelled calls still count against
	// Groq's rate limit, which is why tuning debounce shrinks both.
	CostUSD float64 `json:"cost_usd"`

	// PriceTableVersion identifies the price table cost_usd was computed under
	// (see price.go), so older rows can be told apart after a price
	// correction.
	PriceTableVersion int    `json:"price_table_version"`
	Cancelled         bool   `json:"cancelled"`
	CancelledAtStage  string `json:"cancelled_at_stage"` // "in_flight" | ""

	EventsDroppedSinceLast int64 `json:"events_dropped_since_last"`

	// METRICS(§12): Provider/Model/ErrorType are additive fields from the
	// provider-interface refactor (T1) — Provider/Model let two adapters
	// serving the same model name at different prices be told apart (see
	// price.go's provider+model key); ErrorType is the unwrapped
	// *provider.Error Kind on the error path, empty otherwise. Profile is
	// wired in a later ticket (TOML config profiles) and is always "" here.
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	Profile   string `json:"profile"`
	ErrorType string `json:"error_type"`
}

// SessionID derives the session portion of a request id: everything before
// the last '.' (ids are "<session>.<seq>", minted client-side). This mirrors
// server.shortID's split point but keeps the prefix instead of the suffix;
// duplicated here (rather than imported) because internal/server MUST NOT be
// modified or depended on by the removable metrics package (see design
// principle: removability).
func SessionID(requestID string) string {
	for i := len(requestID) - 1; i >= 0; i-- {
		if requestID[i] == '.' {
			return requestID[:i]
		}
	}
	return requestID
}
