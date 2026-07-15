package metrics

// PriceTableVersion is stamped onto every "request" event as
// price_table_version, so historical rows computed under an older table
// (before a price correction) can be told apart from current ones when
// re-deriving cost_usd during analysis. Bump it whenever the table below
// changes.
//
// v1 -> v2: replaced the guessed flat CachedPerM constant with a derived
// 50%-of-input-rate formula (see cachedDiscount below) and verified/replaced
// the placeholder prices against Groq's official pricing page. This changes
// how cost_usd would be recomputed for any cached tokens, so it's a
// version-worthy change even though the llama-3.3-70b-versatile input/output
// numbers happened to already match.
const PriceTableVersion = 2

// modelPrice holds per-million-token USD pricing for one model.
type modelPrice struct {
	InPerM  float64
	OutPerM float64
}

// cachedDiscount is the fraction of InPerM billed for cached input tokens.
// Per Groq's official prompt-caching docs, cached input tokens are billed at
// a 50% discount off the normal input rate:
// https://console.groq.com/docs/prompt-caching ("a 50% discount for cached
// input tokens"). This is not a guess — it's the documented relationship, so
// we derive the cached rate from InPerM instead of storing a second
// independently-guessed constant.
//
// Note: as of the verification date below, prompt caching is only supported
// on a subset of Groq models (the GPT-OSS family: gpt-oss-20b, gpt-oss-120b,
// gpt-oss-safeguard-20b). llama-3.3-70b-versatile — the daemon's default
// model — does NOT support prompt caching, so cachedTokens (cached_read_tokens)
// will legitimately always be 0 for it; the discount only has an effect once
// a caching-capable model is wired up (see openai/gpt-oss-120b below, the
// likely Phase 2 candidate).
const cachedDiscount = 0.5

// priceTable maps model name -> pricing. Unknown models (CostUSD's default)
// simply cost 0 rather than erroring, since this is a dev-only advisory
// number, not billing.
//
// Prices verified 2026-07-15 against Groq's official pricing page
// (https://groq.com/pricing) and, for gpt-oss-120b, cross-checked against
// its GroqDocs model page (https://console.groq.com/docs/model/openai/gpt-oss-120b),
// both of which independently agreed. Groq pricing moves — re-verify against
// https://groq.com/pricing before citing these numbers again, especially if
// this table is more than a few months old.
var priceTable = map[string]modelPrice{
	// Groq pricing page, "Llama 3.3 70B Versatile 128k" row: $0.59 / $0.79
	// per million input/output tokens.
	"llama-3.3-70b-versatile": {
		InPerM:  0.59,
		OutPerM: 0.79,
	},
	// Groq pricing page, "GPT OSS 120B 128k" row: $0.15 / $0.60 per million
	// input/output tokens; matches the model's own GroqDocs page, which also
	// separately lists the cached-input rate as $0.075/M (= 0.5 * $0.15,
	// consistent with cachedDiscount above).
	"openai/gpt-oss-120b": {
		InPerM:  0.15,
		OutPerM: 0.60,
	},
}

// CostUSD estimates the dollar cost of one request given its model and token
// counts. Unknown models return 0.
func CostUSD(model string, inputTokens, outputTokens, cachedTokens int) float64 {
	p, ok := priceTable[model]
	if !ok {
		return 0
	}
	// cachedTokens is a subset of inputTokens billed at the discounted cached
	// rate instead of InPerM; the remainder of inputTokens bills at the
	// regular rate.
	uncached := inputTokens - cachedTokens
	if uncached < 0 {
		uncached = 0
	}
	cachedPerM := p.InPerM * cachedDiscount
	cost := float64(uncached)/1e6*p.InPerM +
		float64(cachedTokens)/1e6*cachedPerM +
		float64(outputTokens)/1e6*p.OutPerM
	return cost
}
