// Package provider defines the Provider seam and its shared streaming core,
// with three adapters behind one interface (design §6): openai.go (any
// OpenAI-compatible endpoint), anthropic.go (native SDK, the "quality"
// path), codestral.go (hand-rolled Mistral FIM). Adapters render
// prompt.Prompt their own way (chat messages vs. FIM prompt+suffix) but
// share the streaming policy (accum.go) and error taxonomy (errors.go).
// Shared pieces are unexported and live here so adapters can use them
// without an exported API, hence sibling files rather than subpackages.
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
	// RenderPrompt returns the exact text this adapter would send for req, in
	// its own wire shape (chat messages vs. raw FIM prompt+suffix), with no
	// network I/O — for a caller (e.g. eval's report) to capture what the
	// model saw without a live call.
	RenderPrompt(req Request) string
}

// RenderChatPrompt formats a chat-style request as a single human-readable
// "SYSTEM:/USER:" string; shared by openai.go and anthropic.go (and exported
// for other Provider implementations, e.g. eval's StubProvider).
func RenderChatPrompt(req Request) string {
	return "SYSTEM:\n" + req.Prompt.System + "\n\nUSER:\n" + req.Prompt.ChatUser()
}
