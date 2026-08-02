package metrics

// PriceTableVersion is stamped onto every "request" event as
// price_table_version, so rows computed under an older table can be told
// apart when re-deriving cost_usd. Bump it whenever priceTable changes.
const PriceTableVersion = 4

// modelPrice holds per-million-token USD pricing for one provider+model.
type modelPrice struct {
	InPerM  float64
	OutPerM float64
	// CachedPerM is the per-million-token rate for cached input tokens,
	// stored directly since the discount fraction varies by provider (Groq
	// 0.5x InPerM, Anthropic 0.1x).
	CachedPerM float64
}

// priceTable maps "provider/model" -> pricing. Unknown keys cost 0 rather
// than erroring — this is a dev-only advisory number, not billing. Prices
// last verified 2026-07-15; re-check against each provider's pricing page
// before citing these numbers if the table is more than a few months old.
// Only Groq's GPT-OSS family and Anthropic/Codestral support cached-token
// discounts; llama-3.3-70b-versatile legitimately always reports 0 cached.
var priceTable = map[string]modelPrice{
	"openai/llama-3.3-70b-versatile": {
		InPerM:     0.59,
		OutPerM:    0.79,
		CachedPerM: 0.59 * 0.5,
	},
	"openai/openai/gpt-oss-120b": {
		InPerM:     0.15,
		OutPerM:    0.60,
		CachedPerM: 0.15 * 0.5,
	},
	"anthropic/claude-haiku-4-5": {
		InPerM:     1.00,
		OutPerM:    5.00,
		CachedPerM: 0.10,
	},
	"codestral/codestral-latest": {
		InPerM:     0.30,
		OutPerM:    0.90,
		CachedPerM: 0.03,
	},
}

// CostUSD estimates the dollar cost of one request given its provider,
// model, and token counts. Unknown provider/model pairs return 0.
func CostUSD(provider, model string, inputTokens, outputTokens, cachedTokens int) float64 {
	p, ok := priceTable[provider+"/"+model]
	if !ok {
		return 0
	}
	// cachedTokens is a subset of inputTokens billed at CachedPerM; the rest
	// bills at InPerM.
	uncached := inputTokens - cachedTokens
	if uncached < 0 {
		uncached = 0
	}
	cost := float64(uncached)/1e6*p.InPerM +
		float64(cachedTokens)/1e6*p.CachedPerM +
		float64(outputTokens)/1e6*p.OutPerM
	return cost
}
