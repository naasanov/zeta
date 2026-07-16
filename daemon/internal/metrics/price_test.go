package metrics

import "testing"

// TestCostUSD_KnownModel checks a known token count against a hand-computed
// expected cost for the default model, pinning the current priceTable
// numbers (llama-3.3-70b-versatile: $0.59/M in, $0.79/M out).
func TestCostUSD_KnownModel(t *testing.T) {
	// 1,000,000 input tokens, 500,000 output tokens, no cached tokens.
	got := CostUSD("openai", "llama-3.3-70b-versatile", 1_000_000, 500_000, 0)
	want := 1.0*0.59 + 0.5*0.79 // $0.59 + $0.395 = $0.985
	if !floatsClose(got, want) {
		t.Errorf("CostUSD() = %v, want %v", got, want)
	}
}

// TestCostUSD_CachedDiscount asserts cached input tokens are billed at the
// model's CachedPerM rate (50% of InPerM for Groq's gpt-oss-120b, per
// https://console.groq.com/docs/prompt-caching), using openai/gpt-oss-120b
// since it's the priced model that actually supports caching.
func TestCostUSD_CachedDiscount(t *testing.T) {
	const provider = "openai"
	const model = "openai/gpt-oss-120b"

	// 1,000,000 input tokens, of which 400,000 are cached; no output tokens
	// isolates the input-side cached-vs-uncached split.
	got := CostUSD(provider, model, 1_000_000, 0, 400_000)

	inPerM := 0.15
	cachedPerM := priceTable[provider+"/"+model].CachedPerM // 0.075
	want := 0.6*inPerM + 0.4*cachedPerM                     // 0.09 + 0.03 = 0.12
	if !floatsClose(got, want) {
		t.Errorf("CostUSD() = %v, want %v", got, want)
	}

	// Sanity: fully cached input should cost exactly cachedPerM/inPerM of
	// fully uncached input for the same token count.
	allCached := CostUSD(provider, model, 1_000_000, 0, 1_000_000)
	allUncached := CostUSD(provider, model, 1_000_000, 0, 0)
	if !floatsClose(allCached, allUncached*(cachedPerM/inPerM)) {
		t.Errorf("fully cached cost = %v, want %v", allCached, allUncached*(cachedPerM/inPerM))
	}
}

// TestCostUSD_UnknownModelIsZero asserts unknown provider/model pairs return
// 0 cost rather than erroring, since cost_usd is a dev-only advisory number,
// not billing.
func TestCostUSD_UnknownModelIsZero(t *testing.T) {
	got := CostUSD("openai", "some-model-not-in-the-table", 1_000_000, 1_000_000, 500_000)
	if got != 0 {
		t.Errorf("CostUSD() for unknown model = %v, want 0", got)
	}
}

// TestCostUSD_SameModelNameDifferentProviderDoesNotCollide pins the T1
// refactor's whole reason for keying priceTable by provider+model: an
// unpriced provider serving a model name that happens to match a priced
// Groq model must not silently inherit Groq's price.
func TestCostUSD_SameModelNameDifferentProviderDoesNotCollide(t *testing.T) {
	got := CostUSD("anthropic", "llama-3.3-70b-versatile", 1_000_000, 500_000, 0)
	if got != 0 {
		t.Errorf("CostUSD() for unpriced provider/model pair = %v, want 0", got)
	}
}

// TestCostUSD_AnthropicCachedDiscount pins the anthropic/claude-haiku-4-5
// entry: cached tokens bill at 0.1x the input rate (Anthropic's discount),
// not Groq's 0.5x, which is exactly why CachedPerM is a per-model field.
func TestCostUSD_AnthropicCachedDiscount(t *testing.T) {
	const provider = "anthropic"
	const model = "claude-haiku-4-5"

	// 1,000,000 input tokens, of which 400,000 are cached; no output tokens
	// isolates the input-side cached-vs-uncached split.
	got := CostUSD(provider, model, 1_000_000, 0, 400_000)

	inPerM := 1.00
	cachedPerM := priceTable[provider+"/"+model].CachedPerM // 0.10
	want := 0.6*inPerM + 0.4*cachedPerM                     // 0.60 + 0.04 = 0.64
	if !floatsClose(got, want) {
		t.Errorf("CostUSD() = %v, want %v", got, want)
	}
}

// TestCostUSD_Codestral pins the codestral/codestral-latest entry (unverified
// placeholder — see the TODO(price) comment in price.go) so a future price
// correction shows up as an intentional test diff rather than a silent drift.
func TestCostUSD_Codestral(t *testing.T) {
	got := CostUSD("codestral", "codestral-latest", 1_000_000, 500_000, 0)
	want := 1.0*0.30 + 0.5*0.90 // $0.30 + $0.45 = $0.75
	if !floatsClose(got, want) {
		t.Errorf("CostUSD() = %v, want %v", got, want)
	}
}

func floatsClose(a, b float64) bool {
	const eps = 1e-9
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < eps
}
