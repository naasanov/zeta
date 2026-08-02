// This file is the live-provider axis: the first thing in this package that
// makes a real network call. It mirrors cmd/autopilotd/main.go's newProvider
// switch and reuses internal/config's brand-preset table verbatim, so an
// eval run and a production run can never silently diverge on how a
// provider is constructed.
package eval

import (
	"fmt"

	"github.com/naasanov/zsh-autopilot/daemon/internal/config"
	"github.com/naasanov/zsh-autopilot/daemon/internal/provider"
)

// groqPerMinute is kept a hair under groq's advertised 30/min free tier —
// field data showed 19% 429s even before the eval harness's own concurrent
// worker pool adds load.
const groqPerMinute = 25

// NewLiveProvider resolves brand ("codestral"/"anthropic"/"groq"/"ollama", or
// the "openai" escape hatch) into a real provider.Provider, using an empty
// config.Config so only the preset table applies. modelOverride, if
// non-empty, replaces the preset's default model.
//
// A missing required API key is a clear, named, fatal error — never a
// silent fallback to echo/stub output, unlike cmd/autopilotd's degrade path:
// an eval that quietly measured a stub would produce numbers that look real
// and aren't.
//
// fimRenderer comes from the selected Variant and applies only to the
// codestral adapter; nil means the adapter's shipped RenderFIM.
func NewLiveProvider(brand string, modelOverride string, maxTokens int, fimRenderer provider.FIMRenderer) (provider.Provider, error) {
	var cfg config.Config
	resolved, err := cfg.Resolve(brand)
	if err != nil {
		return nil, fmt.Errorf("eval: resolving provider %q: %w", brand, err)
	}
	if modelOverride != "" {
		resolved.Model = modelOverride
	}

	apiKey, err := resolved.ResolveKey()
	if err != nil {
		return nil, fmt.Errorf("eval: resolving API key for provider %q: %w", brand, err)
	}
	needsKey := resolved.APIKeyEnv != "" || resolved.APIKeyCmd != ""
	if needsKey && apiKey == "" {
		return nil, fmt.Errorf("eval: provider %q needs an API key; set %s (or configure api_key_cmd)", brand, resolved.APIKeyEnv)
	}

	p, err := newLiveAdapter(resolved, apiKey, maxTokens, fimRenderer)
	if err != nil {
		return nil, fmt.Errorf("eval: constructing provider %q: %w", brand, err)
	}
	return p, nil
}

// newLiveAdapter mirrors cmd/autopilotd/main.go's newProvider switch
// exactly. It's a deliberate near-duplicate, not a shared helper: neither
// package may import the other (see types.go's "Import invariant"). Re-copy
// from newProvider if this drifts, rather than inventing a third mapping.
// fimRenderer applies to codestral only — the one adapter with a FIM
// rendering step to swap.
func newLiveAdapter(r config.ResolvedProfile, apiKey string, maxTokens int, fimRenderer provider.FIMRenderer) (provider.Provider, error) {
	switch r.Adapter {
	case "openai":
		return provider.NewOpenAI(r.BaseURL, r.Model, apiKey, maxTokens)
	case "anthropic":
		return provider.NewAnthropic(r.Model, apiKey, maxTokens)
	case "codestral":
		// WithFIMRenderer ignores a nil renderer, so the no-variant path
		// still gets RenderFIM without a branch here.
		return provider.NewCodestral(r.BaseURL, r.Model, apiKey, maxTokens, provider.WithFIMRenderer(fimRenderer))
	default:
		return nil, fmt.Errorf("eval: unknown adapter %q", r.Adapter)
	}
}

// LimiterForBrand returns the rate limiter an eval run should use for calls
// to brand: groq is capped at groqPerMinute/min; codestral/anthropic and
// unknown brands get NoopLimiter (concurrency-bounded by the Runner's worker
// pool instead — an unrecognized brand will already have failed in
// NewLiveProvider before a limiter matters).
func LimiterForBrand(brand string) Limiter {
	switch brand {
	case "groq":
		return NewRateLimiter(groqPerMinute)
	default:
		return NoopLimiter{}
	}
}
