package provider

import (
	"strings"
	"time"
)

// accumulator implements the streaming policy shared by every adapter:
// TTFT stamping and the first-line cutoff.
type accumulator struct {
	start   time.Time
	buf     strings.Builder
	ttft    time.Duration
	stopped bool
}

// newAccumulator stamps the send start; call immediately before the
// request goes out so TTFT measures the full round trip.
func newAccumulator(start time.Time) *accumulator {
	return &accumulator{start: start}
}

// Returns stop=true once a newline has been seen; the caller must break
// its read loop there rather than wait for trailing usage stats.
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

func (a *accumulator) Text() string {
	text := a.buf.String()
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		return text[:i]
	}
	return text
}

func (a *accumulator) Raw() string {
	return a.buf.String()
}

func (a *accumulator) TTFT() time.Duration {
	return a.ttft
}
