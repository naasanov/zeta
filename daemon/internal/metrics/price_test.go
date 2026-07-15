package metrics

import "testing"

// TestCostUSD_KnownModel checks a known token count against a hand-computed
// expected cost for the default model, pinning the current priceTable
// numbers (llama-3.3-70b-versatile: $0.59/M in, $0.79/M out).
func TestCostUSD_KnownModel(t *testing.T) {
	// 1,000,000 input tokens, 500,000 output tokens, no cached tokens.
	got := CostUSD("llama-3.3-70b-versatile", 1_000_000, 500_000, 0)
	want := 1.0*0.59 + 0.5*0.79 // $0.59 + $0.395 = $0.985
	if !floatsClose(got, want) {
		t.Errorf("CostUSD() = %v, want %v", got, want)
	}
}

// TestCostUSD_CachedDiscount asserts cached input tokens are billed at
// cachedDiscount (50%) of the normal input rate, per Groq's prompt-caching
// docs (https://console.groq.com/docs/prompt-caching), using
// openai/gpt-oss-120b since it's the priced model that actually supports
// caching.
func TestCostUSD_CachedDiscount(t *testing.T) {
	// 1,000,000 input tokens, of which 400,000 are cached; no output tokens
	// isolates the input-side cached-vs-uncached split.
	got := CostUSD("openai/gpt-oss-120b", 1_000_000, 0, 400_000)

	inPerM := 0.15
	cachedPerM := inPerM * cachedDiscount // 0.075
	want := 0.6*inPerM + 0.4*cachedPerM   // 0.09 + 0.03 = 0.12
	if !floatsClose(got, want) {
		t.Errorf("CostUSD() = %v, want %v", got, want)
	}

	// Sanity: fully cached input should cost exactly half of fully uncached
	// input for the same token count.
	allCached := CostUSD("openai/gpt-oss-120b", 1_000_000, 0, 1_000_000)
	allUncached := CostUSD("openai/gpt-oss-120b", 1_000_000, 0, 0)
	if !floatsClose(allCached, allUncached*cachedDiscount) {
		t.Errorf("fully cached cost = %v, want %v (= %v * cachedDiscount)", allCached, allUncached*cachedDiscount, allUncached)
	}
}

// TestCostUSD_UnknownModelIsZero asserts unknown models return 0 cost rather
// than erroring, since cost_usd is a dev-only advisory number, not billing.
func TestCostUSD_UnknownModelIsZero(t *testing.T) {
	got := CostUSD("some-model-not-in-the-table", 1_000_000, 1_000_000, 500_000)
	if got != 0 {
		t.Errorf("CostUSD() for unknown model = %v, want 0", got)
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
