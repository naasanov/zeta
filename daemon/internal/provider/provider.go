// Package provider defines the Provider seam and its shared streaming core,
// with three adapters behind one interface (design §6): openai.go (any
// OpenAI-compatible endpoint), anthropic.go (native SDK, the "quality"
// path), codestral.go (hand-rolled Mistral FIM). Adapters render a
// prompt.Prompt (via its ChatPrompt/FIMPrompt shape) from a protocol.Request
// their own way (chat messages vs. FIM prompt+suffix) but share the
// streaming policy (accum.go) and error taxonomy (errors.go).
// Shared pieces are unexported and live here so adapters can use them
// without an exported API, hence sibling files rather than subpackages.
package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/naasanov/zsh-autopilot/daemon/internal/config"
	"github.com/naasanov/zsh-autopilot/daemon/internal/prompt"
	"github.com/naasanov/zsh-autopilot/daemon/internal/protocol"
)

// Request is one completion request to a Provider: the raw protocol request
// (rendered into wire shape by the provider's own prompt) plus the
// max-tokens cap for this call.
type Request struct {
	Req       protocol.Request
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
	// PromptName identifies the prompt this provider renders with, for
	// METRICS(§12) and eval reporting.
	PromptName() string
	// RenderPrompt returns the exact text this adapter would send for req, in
	// its own wire shape (chat messages vs. raw FIM prompt+suffix), with no
	// network I/O — for a caller (e.g. eval's report) to capture what the
	// model saw without a live call.
	RenderPrompt(req Request) string
}

// RenderChatPrompt formats a rendered chat payload as a single
// human-readable "SYSTEM:/USER:" string; shared by openai.go and
// anthropic.go (and exported for other Provider implementations, e.g.
// eval's StubProvider).
func RenderChatPrompt(pl prompt.ChatPayload) string {
	return "SYSTEM:\n" + pl.System + "\n\nUSER:\n" + pl.User
}

// NewFromProfile constructs a Provider from a resolved profile, switching on
// the internal Adapter, not the user-facing brand — several brands share an
// adapter (groq/ollama both speak "openai"). p must be shaped for the
// resolved adapter (ChatPrompt for openai/anthropic, FIMPrompt for
// codestral); a mismatch is a config/wiring error, reported clearly rather
// than panicking.
func NewFromProfile(r config.ResolvedProfile, apiKey string, maxTokens int, p prompt.Prompt) (Provider, error) {
	switch r.Adapter {
	case "openai":
		cp, ok := p.(prompt.ChatPrompt)
		if !ok {
			return nil, fmt.Errorf("provider: prompt %q is not chat-shaped; adapter %q needs a chat prompt", p.Name(), r.Adapter)
		}
		return NewOpenAI(r.BaseURL, r.Model, apiKey, maxTokens, cp)
	case "anthropic":
		cp, ok := p.(prompt.ChatPrompt)
		if !ok {
			return nil, fmt.Errorf("provider: prompt %q is not chat-shaped; adapter %q needs a chat prompt", p.Name(), r.Adapter)
		}
		// Anthropic's constructor takes no baseURL; r.BaseURL (if set) is
		// ignored for this provider.
		return NewAnthropic(r.Model, apiKey, maxTokens, cp)
	case "codestral":
		fp, ok := p.(prompt.FIMPrompt)
		if !ok {
			return nil, fmt.Errorf("provider: prompt %q is not FIM-shaped; adapter %q needs a FIM prompt", p.Name(), r.Adapter)
		}
		return NewCodestral(r.BaseURL, r.Model, apiKey, maxTokens, fp)
	default:
		// config.Resolve only ever fills Adapter from presets or the openai
		// escape hatch, so reaching here means a programmer error, not user
		// input.
		return nil, fmt.Errorf("provider: unknown adapter %q", r.Adapter)
	}
}
