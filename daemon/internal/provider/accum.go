package provider

import (
	"strings"
	"time"
)

// accumulator implements the streaming policy shared by every adapter: TTFT
// stamping and the first-line cutoff (design §4 "stream + take first line
// only"). Adapters feed it text deltas as they arrive off the wire; it does
// not know anything about HTTP, SSE, or a specific provider's chunk shape.
type accumulator struct {
	start   time.Time
	buf     strings.Builder
	ttft    time.Duration
	stopped bool
}

// newAccumulator stamps the send start; call it immediately before the
// request goes out so TTFT measures the full round trip to first byte.
func newAccumulator(start time.Time) *accumulator {
	return &accumulator{start: start}
}

// Push appends a text delta. It stamps TTFT on the first non-empty delta
// (empty deltas — e.g. a chunk carrying only finish_reason or usage — must
// not stamp it). It returns stop=true once a newline has been seen in the
// accumulated text; the caller MUST break out of its stream loop and return
// at that point rather than keep reading to collect trailing usage stats —
// that's the whole point of the cutoff. Once stopped, further Push calls are
// no-ops that keep returning stop=true.
func (a *accumulator) Push(delta string) (stop bool) {
	if a.stopped {
		return true
	}
	if delta != "" && a.ttft == 0 {
		a.ttft = time.Since(a.start)
	}
	a.buf.WriteString(delta)
	if strings.ContainsRune(a.buf.String(), '\n') {
		a.stopped = true
	}
	return a.stopped
}

// Text returns the accumulated text truncated at the first newline seen, or
// the full accumulation if no newline was ever seen.
func (a *accumulator) Text() string {
	text := a.buf.String()
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		return text[:i]
	}
	return text
}

// TTFT returns the time from newAccumulator's start to the first non-empty
// delta, or 0 if no non-empty delta has been pushed yet.
func (a *accumulator) TTFT() time.Duration {
	return a.ttft
}
