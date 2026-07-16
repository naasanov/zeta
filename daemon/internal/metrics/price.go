package metrics

// PriceTableVersion is stamped onto every "request" event as
// price_table_version, so historical rows computed under an older table
// (before a price correction) can be told apart from current ones when
// re-deriving cost_usd during analysis. Bump it whenever the table below
// changes.
//
// v1 -> v2: replaced the guessed flat CachedPerM constant with a derived
// 50%-of-input-rate formula and verified/replaced the placeholder prices
// against Groq's official pricing page. This changes how cost_usd would be
// recomputed for any cached tokens, so it's a version-worthy change even
// though the llama-3.3-70b-versatile input/output numbers happened to
// already match.
//
// v2 -> v3: METRICS(§12) — rekeyed priceTable by "provider/model" instead of
// bare model name (two providers can serve the same model name at different
// prices) and replaced the global cachedDiscount constant with a per-model
// CachedPerM field (Anthropic's cached rate is 0.1x input, not Groq's 0.5x,
// so a single global fraction can't represent both once a second provider is
// wired up).
const PriceTableVersion = 3

// modelPrice holds per-million-token USD pricing for one provider+model.
type modelPrice struct {
	InPerM  float64
	OutPerM float64
	// CachedPerM is the per-million-token USD rate for cached input tokens,
	// stored directly (not derived from InPerM) since the cached discount
	// varies by provider: Groq documents a flat 50% discount off InPerM for
	// the models that support caching; a later Anthropic entry bills cached
	// reads at 0.1x InPerM instead, which a single global fraction can't
	// represent.
	CachedPerM float64
}

// priceTable maps "provider/model" -> pricing. Unknown keys (CostUSD's
// default) simply cost 0 rather than erroring, since this is a dev-only
// advisory number, not billing.
//
// Prices verified 2026-07-15 against Groq's official pricing page
// (https://groq.com/pricing) and, for gpt-oss-120b, cross-checked against
// its GroqDocs model page (https://console.groq.com/docs/model/openai/gpt-oss-120b),
// both of which independently agreed. Groq pricing moves — re-verify against
// https://groq.com/pricing before citing these numbers again, especially if
// this table is more than a few months old.
//
// Note: as of the verification date above, prompt caching is only supported
// on a subset of Groq models (the GPT-OSS family: gpt-oss-20b, gpt-oss-120b,
// gpt-oss-safeguard-20b). llama-3.3-70b-versatile — the daemon's default
// model — does NOT support prompt caching, so cachedTokens (cached_read_tokens)
// will legitimately always be 0 for it; CachedPerM only has an effect once a
// caching-capable model is wired up (see openai/gpt-oss-120b below, the
// likely Phase 2 candidate).
var priceTable = map[string]modelPrice{
	// Groq pricing page, "Llama 3.3 70B Versatile 128k" row: $0.59 / $0.79
	// per million input/output tokens. Cached rate per Groq's prompt-caching
	// docs (https://console.groq.com/docs/prompt-caching): 50% discount off
	// InPerM.
	"openai/llama-3.3-70b-versatile": {
		InPerM:     0.59,
		OutPerM:    0.79,
		CachedPerM: 0.59 * 0.5,
	},
	// Groq pricing page, "GPT OSS 120B 128k" row: $0.15 / $0.60 per million
	// input/output tokens; matches the model's own GroqDocs page, which also
	// separately lists the cached-input rate as $0.075/M (= 0.5 * $0.15).
	"openai/openai/gpt-oss-120b": {
		InPerM:     0.15,
		OutPerM:    0.60,
		CachedPerM: 0.15 * 0.5,
	},
	// Anthropic's published Claude Haiku 4.5 pricing: $1.00 / $5.00 per
	// million input/output tokens. Unlike Groq's flat 50%-of-input cached
	// rate above, Anthropic discounts cached reads to 0.1x the input rate —
	// hence the per-model CachedPerM field rather than a global fraction.
	"anthropic/claude-haiku-4-5": {
		InPerM:     1.00,
		OutPerM:    5.00,
		CachedPerM: 0.10,
	},
	// TODO(price): Codestral rate unverified — confirm against Mistral
	// pricing before treating cost_usd for this row as anything but a rough
	// placeholder.
	"codestral/codestral-latest": {
		InPerM:     0.30,
		OutPerM:    0.90,
		CachedPerM: 0,
	},
}

// CostUSD estimates the dollar cost of one request given its provider,
// model, and token counts. Unknown provider/model pairs return 0.
func CostUSD(provider, model string, inputTokens, outputTokens, cachedTokens int) float64 {
	p, ok := priceTable[provider+"/"+model]
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
	cost := float64(uncached)/1e6*p.InPerM +
		float64(cachedTokens)/1e6*p.CachedPerM +
		float64(outputTokens)/1e6*p.OutPerM
	return cost
}
