// Package provider defines the Provider seam, with three adapters (openai,
// anthropic, codestral) behind one shared interface.
package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/naasanov/zsh-autopilot/daemon/internal/config"
	"github.com/naasanov/zsh-autopilot/daemon/internal/prompt"
	"github.com/naasanov/zsh-autopilot/daemon/internal/protocol"
)

type Request struct {
	Req       protocol.Request
	MaxTokens int
}

// HTTPStatus is populated even on error returns, so the caller can log the
// status of a failed call.
type Completion struct {
	Text         string
	TTFT         time.Duration
	InputTokens  int
	OutputTokens int
	CachedTokens int
	HTTPStatus   int
	StopReason   string
	RateLimit    *RateLimit // nil if the endpoint sent no rate-limit headers
}

// RateLimit reports a provider's token-bucket rate-limit state as observed
// from response headers.
type RateLimit struct {
	LimitTokens     int
	RemainingTokens int
	ResetTokens     time.Duration
	RetryAfter      time.Duration
}

type Provider interface {
	Complete(ctx context.Context, req Request) (Completion, error)
	Name() string // "openai" | "anthropic" | "codestral"
	Model() string
	PromptName() string
	// RenderPrompt returns the exact text this adapter would send for req,
	// in its own wire shape, with no network I/O.
	RenderPrompt(req Request) string
}

func RenderChatPrompt(pl prompt.ChatPayload) string {
	return "SYSTEM:\n" + pl.System + "\n\nUSER:\n" + pl.User
}

// NewFromProfile constructs a Provider from a resolved profile, switching
// on Adapter, not brand (groq/ollama both speak "openai"). p must match the
// adapter's prompt shape; a mismatch errors clearly rather than panicking.
func NewFromProfile(r config.ResolvedProfile, apiKey string, maxTokens int, p prompt.Prompt) (Provider, error) {
	if r.MaxTokens != 0 {
		maxTokens = r.MaxTokens
	}

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
		return nil, fmt.Errorf("provider: unknown adapter %q", r.Adapter)
	}
}
