// This file is Part 4 of .docs/eval_harness_plan.md: the live-provider axis.
// It is the first thing in this package that can make a real network call —
// everything through Part 3 drove StubProvider only. It deliberately
// reimplements no provider-construction logic: it mirrors
// daemon/cmd/autopilotd/main.go's newProvider switch (Adapter -> constructor)
// and reuses internal/config's brand-preset table verbatim, because
// duplicating that mapping with different behavior is exactly how an eval
// run and a production run end up silently measuring two different things.
package eval

import (
	"fmt"

	"github.com/naasanov/zsh-autopilot/daemon/internal/config"
	"github.com/naasanov/zsh-autopilot/daemon/internal/provider"
)

// groqPerMinute is the plan doc's rate-limit line: "groq 25/min (free tier is
// 30/min and we've already seen 19% 429s in the field)". Kept a hair under
// the advertised limit deliberately, not equal to it — the field data showed
// 429s even without the eval harness's own concurrent worker pool adding
// load on top.
const groqPerMinute = 25

// NewLiveProvider resolves brand (a config preset name — "codestral",
// "anthropic", "groq", "ollama" — or the "openai" escape hatch, exactly as
// accepted by config.Config.Resolve) into a real provider.Provider, using an
// empty config.Config so only the preset table applies (an eval run has no
// config.toml profile of its own; -model is how a run pins something a
// profile would otherwise override).
//
// modelOverride, if non-empty, replaces the preset's default model — the
// plan doc's "pin the model per run, record it" rule. Pass "" to keep the
// preset default.
//
// A missing required API key is a clear, named error — "set
// ZSH_AUTOPILOT_<BRAND>_KEY" — never a silent fallback to echo or stub
// output. Unlike cmd/autopilotd (which degrades to echo mode so a shell
// keeps working without ghost text), an eval that quietly measured a stub
// would produce numbers that look real and are not; there is no analogous
// safe degradation here, so this is fatal to the caller.
//
// fimRenderer comes from the selected Variant (Variant.FIMRenderer) and is
// applied only to the codestral adapter; nil means the adapter's shipped
// RenderFIM. See Variant.FIMRenderer on why non-FIM adapters ignore it.
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
// (Adapter -> constructor) exactly. It is a deliberate near-duplicate, not a
// shared helper, because internal/eval cannot import cmd/autopilotd (a main
// package) and cmd/autopilotd must not import internal/eval (see
// types.go's "Import invariant"). If this ever drifts from newProvider,
// re-copy it from there rather than inventing a third mapping.
// fimRenderer applies to the codestral case only — it is the one adapter
// with a FIM rendering step to swap. Passing it to the others would have
// nothing to bind to, which is exactly why Variant.FIMRenderer is documented
// as a no-op outside a codestral cell rather than an error.
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
// to brand, per the plan doc's "Rate limiting" section: groq is capped at
// groqPerMinute/min (its free tier's 30/min already produced 19% 429s in the
// field per [[metrics-findings]]); codestral/anthropic are treated as
// "unlimited but concurrency-bounded" — the Runner's own worker pool
// (defaultConcurrency) is what bounds their concurrency, not a request-rate
// token bucket. Unknown brands also get NoopLimiter: an unrecognized brand
// will already have failed in NewLiveProvider before a limiter is ever
// needed, so this just avoids a second place that has to enumerate brands.
func LimiterForBrand(brand string) Limiter {
	switch brand {
	case "groq":
		return NewRateLimiter(groqPerMinute)
	default:
		return NoopLimiter{}
	}
}
