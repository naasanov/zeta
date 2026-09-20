package metrics

import "testing"

func TestCostUSD_KnownModel(t *testing.T) {
	// 1,000,000 input tokens, 500,000 output tokens, no cached tokens.
	got := CostUSD("openai", "openai/gpt-oss-20b", 1_000_000, 500_000, 0)
	want := 1.0*0.075 + 0.5*0.30 // $0.075 + $0.15 = $0.225
	if !floatsClose(got, want) {
		t.Errorf("CostUSD() = %v, want %v", got, want)
	}
}

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

func TestCostUSD_UnknownModelIsZero(t *testing.T) {
	got := CostUSD("openai", "some-model-not-in-the-table", 1_000_000, 1_000_000, 500_000)
	if got != 0 {
		t.Errorf("CostUSD() for unknown model = %v, want 0", got)
	}
}

func TestCostUSD_SameModelNameDifferentProviderDoesNotCollide(t *testing.T) {
	got := CostUSD("anthropic", "llama-3.3-70b-versatile", 1_000_000, 500_000, 0)
	if got != 0 {
		t.Errorf("CostUSD() for unpriced provider/model pair = %v, want 0", got)
	}
}

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
