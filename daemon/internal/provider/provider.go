// Package provider defines the Provider seam and its shared streaming core.
// Three adapters implement it: the hand-rolled OpenAI-compatible client
// (Groq et al., this ticket), a native Anthropic adapter, and a Codestral
// FIM adapter (both later, parallel tickets — design §6). Adapters render
// prompt.Prompt their own way (chat messages vs. prompt+suffix) but share the
// accumulator's streaming policy (TTFT stamping, first-line cutoff) and the
// Error/ErrKind classification in errors.go.
package provider

import (
	"context"
	"time"

	"github.com/naasanov/zsh-autopilot/daemon/internal/prompt"
)

// Request is one completion request to a Provider: the provider-neutral
// prompt plus the max-tokens cap for this call.
type Request struct {
	Prompt    prompt.Prompt
	MaxTokens int
}

// Completion is the result of a successful (or partially successful, on the
// HTTP-error path) Complete call: the suggestion text plus METRICS(§12)
// provider-internal stats used to build the "request" event. HTTPStatus is
// populated even on error returns so the caller can log the status of a
// failed call.
type Completion struct {
	Text         string
	TTFT         time.Duration
	InputTokens  int
	OutputTokens int
	CachedTokens int
	HTTPStatus   int
	StopReason   string
}

// Provider is the only seam the rest of the daemon programs against. A
// provider need NOT use the shared streaming helpers in accum.go — they are
// opt-in, not part of the interface contract.
type Provider interface {
	Complete(ctx context.Context, req Request) (Completion, error)
	Name() string // "openai" | "anthropic" | "codestral" — metrics + price key
	Model() string
}
