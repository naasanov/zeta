// Package provider defines the Provider seam and its shared streaming core,
// with three adapters behind one interface (design §6). Adapters render
// prompt.Prompt their own way (chat messages vs. FIM prompt+suffix) but share
// the same streaming policy and error taxonomy.
//
// File map:
//   - provider.go   — the contract: Provider interface, Request, Completion.
//   - accum.go      — shared accumulator: TTFT stamping + first-line cutoff,
//     driven by each adapter from its own stream.
//   - errors.go     — shared *Error / ErrKind / ClassifyHTTP (→ §12 error_type).
//   - httpclient.go — shared keep-alive http.Client (opt-in; anthropic's SDK
//     owns its own, so it doesn't use this).
//   - openai.go     — openai-go SDK adapter; any OpenAI-compatible endpoint.
//   - anthropic.go  — native anthropic-sdk-go adapter (the "quality" path).
//   - codestral.go  — hand-rolled Mistral FIM adapter (prompt+suffix), plus
//     RenderFIM and the shell-command cutoff logic.
//
// The shared pieces are unexported and live in this one package precisely so
// the adapters can use them without an exported API — which is why the
// adapters are sibling files here rather than subpackages.
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
