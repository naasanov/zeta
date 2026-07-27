// validate.go is Part 5's judge-validation harness (plan doc "The judge" ->
// "Validating the judge before trusting it"): the build half of the
// non-negotiable rule that NO judged number (C3/E3/E7/F2) may be quoted
// until a candidate judge model has been measured at >=90% agreement with
// hand-written human labels on the SAME samples, and that the judge is
// picked empirically by agreement-per-dollar, not by reputation.
//
// # Reusing the production JudgeInput path
//
// judge.go's judgeGrader.Grade builds JudgeInput{Rubric: g.rubric, Req: in,
// Suggestion: suggestion} inline — a three-field struct literal, not a
// separate constructor function. There is nothing to call to "reuse" beyond
// that literal itself, so this file gets at the same two ingredients
// (rubric text, assertion label) the production grader closes over by
// reading them directly off the *judgeGrader Cases() already built for the
// case (judgedAssertion, below) — same package, so the unexported fields are
// visible — rather than re-typing rubric strings here where they could
// silently drift from cases.go. The Suggestion field is the label's
// Suggestion verbatim: per the pinned label-file format, that string IS the
// full resulting command line (what fullCommand(in, out) would have
// produced), exactly what judgeGrader.Grade passes as JudgeInput.Suggestion.
//
// # Cost estimation caveat
//
// The Judge interface (judge.go) returns only a JudgeVerdict — it does not
// surface token usage, and this file's scope forbids editing judge.go to add
// it. So cost here is always the price-table estimate (judgeInputPriceTable
// below) applied to a rough token count of the exact rendered prompt
// (judgeSystemPrompt + renderJudgeUser, the same functions the production
// judge uses to build its request), not a real usage figure from the API
// response. Good enough for ranking candidates against each other — the
// plan doc's "agreement-per-dollar" comparison only needs relative cost.
package eval

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
)

// DefaultJudgeLabelsPath is where -judge-validate looks for hand labels when
// -labels isn't passed, relative to the daemon module root (cmd/eval is
// always run as `cd daemon && go run ./cmd/eval ...`, per every other
// example in the plan doc and README).
const DefaultJudgeLabelsPath = "internal/eval/testdata/judge_labels.jsonl"

// ---- Label file -------------------------------------------------------------

// Label is one hand-written human judgement, loaded from the pinned
// judge_labels.jsonl format (see the plan doc's "Validate before trusting"
// section and this package's own file for the exact schema).
type Label struct {
	CaseID     string
	Suggestion string
	Verdict    string // "pass" or "fail" — the human's call
	Note       string // free text, human-readable only, never interpreted
}

// labelLine is the wire shape of one judge_labels.jsonl row.
type labelLine struct {
	CaseID     string `json:"case_id"`
	Suggestion string `json:"suggestion"`
	Verdict    string `json:"verdict"`
	Note       string `json:"note"`
}

// LoadLabels reads and validates a judge_labels.jsonl stream against cases
// (normally eval.Cases()). Every row is checked eagerly and any problem is a
// fatal error naming the offending line — a typo'd case_id that silently
// scored nothing is exactly the failure this harness exists to prevent, so
// there is no "skip and warn" path here, unlike JudgeCache's corrupt-entry
// handling (that cache is a pure optimization; this is ground truth).
//
// Blank lines and lines starting with "#" are skipped, so the file can carry
// human annotations.
func LoadLabels(r io.Reader, cases []Case) ([]Label, error) {
	caseIndex := make(map[string]Case, len(cases))
	for _, c := range cases {
		caseIndex[c.ID] = c
	}

	var labels []Label
	scanner := bufio.NewScanner(r)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		var raw labelLine
		dec := json.NewDecoder(strings.NewReader(line))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&raw); err != nil {
			return nil, fmt.Errorf("eval: judge labels line %d: invalid JSON: %w", lineNo, err)
		}

		c, ok := caseIndex[raw.CaseID]
		if !ok {
			return nil, fmt.Errorf("eval: judge labels line %d: case_id %q does not match any case in Cases()", lineNo, raw.CaseID)
		}
		if _, _, judged := judgedAssertion(c); !judged {
			return nil, fmt.Errorf("eval: judge labels line %d: case_id %q exists but is not a judge-graded case (only C3/E3/E7/F2 take hand labels)", lineNo, raw.CaseID)
		}
		if raw.Verdict != "pass" && raw.Verdict != "fail" {
			return nil, fmt.Errorf("eval: judge labels line %d: verdict must be \"pass\" or \"fail\", got %q", lineNo, raw.Verdict)
		}

		labels = append(labels, Label{
			CaseID:     raw.CaseID,
			Suggestion: raw.Suggestion,
			Verdict:    raw.Verdict,
			Note:       raw.Note,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("eval: reading judge labels: %w", err)
	}
	return labels, nil
}

// judgedAssertion finds the judge-backed Assertion on c, if any, and returns
// the exact label/rubric the production grader (defaultJudgeGrader in
// cases.go) closes over. This is the single place validate.go reaches into
// judge.go's unexported *judgeGrader shape, so that rubric text lives in
// exactly one place (cases.go) and can never drift between what's graded for
// real and what's graded during validation.
func judgedAssertion(c Case) (label, rubric string, ok bool) {
	for _, a := range c.Asserts {
		if jg, isJudge := a.Grader.(*judgeGrader); isJudge {
			return jg.label, jg.rubric, true
		}
	}
	return "", "", false
}

// ---- Validation ---------------------------------------------------------

// Disagreement is one sample where a candidate judge's verdict didn't match
// the human label, carried with enough detail (case, suggestion, both
// verdicts, the judge's stated reason) to read back and decide whether the
// JUDGE was wrong or the RUBRIC was ambiguous — the entire point of falling
// below the 90% gate.
type Disagreement struct {
	CaseID       string
	Suggestion   string
	HumanVerdict string
	JudgeVerdict string
	JudgeReason  string
}

// JudgeScore is one candidate judge's measured performance against the
// human label set.
type JudgeScore struct {
	Judge string // Judge.Name(), i.e. the model id
	Model string // same as Judge, kept as its own field for report clarity

	Agreed int // judge verdict == human verdict
	Total  int // labels the judge actually produced a verdict for (excludes Errors)

	Agreement float64 // Agreed/Total; 0 if Total==0
	Kappa     float64 // Cohen's kappa vs the human labels, over the same Total

	// Errors counts judge calls that themselves failed (network error,
	// malformed response, ctx cancellation — anything parseVerdict/Judge
	// surfaced as an error). These are NOT disagreements and must never be
	// folded into Total/Agreement: a judge erroring on half the samples must
	// not look like a judge that disagrees on half of them.
	Errors int

	// FirstError carries the first judge-call error's message (truncated,
	// same as AssertionResult.FirstGraderError), so a candidate that shows
	// "0.0% (0/0)" with errors>0 in the scoreboard is diagnosable without a
	// separate manual API call — this is exactly what a wrong model id
	// (404, reads like an auth failure) previously hid.
	FirstError string

	InputTokens  int
	OutputTokens int
	CostUSD      float64 // price-table estimate; see package doc's cost caveat

	Disagreements []Disagreement
}

// ValidateJudges scores each of judges against labels, building JudgeInput
// exactly as the production grader would (same rubric, same Req, same
// Suggestion text) via judgedAssertion. ctx is forwarded to every Judge call
// so the whole validation run stays cancellable like any other network-bound
// eval operation.
//
// Callers are responsible for constructing judges with a bypassed verdict
// cache (or none at all) — ValidateJudges never touches JudgeCache itself,
// so there is nothing here that could accidentally score cached verdicts
// left over from a previous rubric. See NewJudgeCache's bypass parameter.
func ValidateJudges(ctx context.Context, labels []Label, cases []Case, judges []Judge) []JudgeScore {
	caseIndex := make(map[string]Case, len(cases))
	for _, c := range cases {
		caseIndex[c.ID] = c
	}

	scores := make([]JudgeScore, 0, len(judges))
	for _, j := range judges {
		scores = append(scores, scoreJudge(ctx, j, labels, caseIndex))
	}
	return scores
}

func scoreJudge(ctx context.Context, j Judge, labels []Label, caseIndex map[string]Case) JudgeScore {
	score := JudgeScore{Judge: j.Name(), Model: j.Name()}

	// 2x2 confusion matrix (human x judge) for Cohen's kappa: aa = both
	// pass, bb = human pass/judge fail, cc = human fail/judge pass, dd =
	// both fail.
	var aa, bb, cc, dd int

	for _, lab := range labels {
		c, ok := caseIndex[lab.CaseID]
		if !ok {
			// LoadLabels already validated every label against this exact
			// case set; a caller passing a different `cases` here than it
			// used for LoadLabels is a caller bug, not a data problem, so
			// this row is skipped rather than silently mis-scored.
			continue
		}
		_, rubric, judged := judgedAssertion(c)
		if !judged {
			continue
		}

		in := JudgeInput{Rubric: rubric, Req: c.Req, Suggestion: lab.Suggestion}

		if err := ctx.Err(); err != nil {
			score.Errors++
			if score.FirstError == "" {
				score.FirstError = truncateGraderError(err.Error())
			}
			continue
		}

		verdict, err := j.Judge(ctx, in)
		if err != nil {
			score.Errors++
			if score.FirstError == "" {
				score.FirstError = truncateGraderError(err.Error())
			}
			continue
		}

		score.InputTokens += estimateTokens(judgeSystemPrompt + "\n" + renderJudgeUser(in))
		score.OutputTokens += estimateTokens(fmt.Sprintf(`{"verdict":%q,"reason":%q}`, verdictWord(verdict.Pass), verdict.Reason))

		humanPass := lab.Verdict == "pass"
		score.Total++
		if humanPass == verdict.Pass {
			score.Agreed++
		} else {
			score.Disagreements = append(score.Disagreements, Disagreement{
				CaseID:       lab.CaseID,
				Suggestion:   lab.Suggestion,
				HumanVerdict: lab.Verdict,
				JudgeVerdict: verdictWord(verdict.Pass),
				JudgeReason:  verdict.Reason,
			})
		}

		switch {
		case humanPass && verdict.Pass:
			aa++
		case humanPass && !verdict.Pass:
			bb++
		case !humanPass && verdict.Pass:
			cc++
		default:
			dd++
		}
	}

	score.Agreement = safeDiv(score.Agreed, score.Total)
	score.Kappa = cohenKappa(aa, bb, cc, dd)
	score.CostUSD = estimateCost(j.Name(), score.InputTokens, score.OutputTokens)
	return score
}

func verdictWord(pass bool) string {
	if pass {
		return "pass"
	}
	return "fail"
}

func safeDiv(num, den int) float64 {
	if den == 0 {
		return 0
	}
	return float64(num) / float64(den)
}

// cohenKappa computes Cohen's kappa for a 2x2 confusion matrix between two
// raters (human, judge): aa=both-pass, bb=human-pass/judge-fail,
// cc=human-fail/judge-pass, dd=both-fail. Kappa corrects raw agreement for
// the agreement expected by chance given each rater's marginal — the plan
// doc's explicit reason to report it alongside Agreement: a judge that
// always says "fail" on a lopsided 27/30-fail label set scores 90% raw
// agreement while being worthless, and kappa ~0 is what exposes that.
//
// pe==1 (both raters' marginals are degenerate, e.g. every label AND every
// verdict is "fail") makes the standard formula's denominator zero; that
// case is treated as kappa=1 if observed agreement is also perfect (there is
// no disagreement to explain) and kappa=0 otherwise (can't happen
// arithmetically, but guards against surprises from float rounding).
func cohenKappa(aa, bb, cc, dd int) float64 {
	n := aa + bb + cc + dd
	if n == 0 {
		return 0
	}
	total := float64(n)
	po := float64(aa+dd) / total

	humanPass := float64(aa + bb)
	humanFail := float64(cc + dd)
	judgePass := float64(aa + cc)
	judgeFail := float64(bb + dd)

	pe := (humanPass*judgePass + humanFail*judgeFail) / (total * total)

	denom := 1 - pe
	if denom == 0 {
		if po == 1 {
			return 1
		}
		return 0
	}
	return (po - pe) / denom
}

// estimateTokens is a rough chars/4 token-count heuristic (the common
// English-text approximation) — see the package doc's "Cost estimation
// caveat" for why this isn't a real usage figure.
func estimateTokens(s string) int {
	if s == "" {
		return 0
	}
	return (len(s) + 3) / 4
}

// judgeModelPrice is USD per million tokens, input/output, for a judge model
// id. Used only as the cost-estimate fallback described in the package doc.
type judgeModelPrice struct {
	InPerM  float64
	OutPerM float64
}

// judgeInputPriceTable mirrors the plan doc's judge pricing table ("The
// judge" section). Prices are the same directional, vendor-sourced numbers
// cited there — re-check against each vendor's own pricing page before
// quoting a budget from this, same caveat the plan doc states for itself.
// An unknown model id prices at 0 (an advisory number, not billing) rather
// than erroring, so validating an unlisted model still produces an
// agreement score.
var judgeInputPriceTable = map[string]judgeModelPrice{
	"gemini-3.5-flash-lite": {InPerM: 0.30, OutPerM: 2.50},
	"gemini-3.1-flash-lite": {InPerM: 0.25, OutPerM: 1.50},
	"gemini-2.5-flash-lite": {InPerM: 0.10, OutPerM: 0.40},
	"gpt-5-mini":            {InPerM: 0.40, OutPerM: 3.00},
	"claude-haiku-4.5":      {InPerM: 1.00, OutPerM: 5.00},
}

func estimateCost(model string, inputTokens, outputTokens int) float64 {
	price, ok := judgeInputPriceTable[model]
	if !ok {
		return 0
	}
	return float64(inputTokens)/1e6*price.InPerM + float64(outputTokens)/1e6*price.OutPerM
}

// agreementPerDollar is the plan doc's stated selection criterion. A
// zero-cost score (unpriced model, or zero tokens) with nonzero agreement
// ranks as infinitely good rather than crashing the sort; a zero-cost,
// zero-agreement score ranks as 0, not infinite, so an unpriced judge that
// never agreed doesn't erroneously sort to the top.
func agreementPerDollar(s JudgeScore) float64 {
	if s.CostUSD <= 0 {
		if s.Agreement > 0 {
			return math.Inf(1)
		}
		return 0
	}
	return s.Agreement / s.CostUSD
}

// RankByAgreementPerDollar returns a copy of scores sorted best-first by
// agreement-per-dollar (the plan doc's stated selection criterion — "Pick by
// agreement-per-dollar against our own labels", NOT by reputation or raw
// agreement alone). Ties break by higher raw Agreement, then by Judge name
// for a fully deterministic order.
func RankByAgreementPerDollar(scores []JudgeScore) []JudgeScore {
	ranked := make([]JudgeScore, len(scores))
	copy(ranked, scores)
	sort.SliceStable(ranked, func(i, k int) bool {
		pi, pk := agreementPerDollar(ranked[i]), agreementPerDollar(ranked[k])
		if pi != pk {
			return pi > pk
		}
		if ranked[i].Agreement != ranked[k].Agreement {
			return ranked[i].Agreement > ranked[k].Agreement
		}
		return ranked[i].Judge < ranked[k].Judge
	})
	return ranked
}

// LabelImbalance reports the fraction of labels marked "pass" and whether
// the label set is heavily imbalanced (>=80% one verdict) — the condition
// under which raw Agreement alone is misleading and Kappa must be read
// instead (plan doc: "a judge that always says fail scores 90% while being
// worthless"). threshold is fixed at 0.80 rather than exposed as a
// parameter: it's a reporting heuristic, not a tunable knob.
func LabelImbalance(labels []Label) (fracPass float64, imbalanced bool) {
	if len(labels) == 0 {
		return 0, false
	}
	var pass int
	for _, l := range labels {
		if l.Verdict == "pass" {
			pass++
		}
	}
	fracPass = float64(pass) / float64(len(labels))
	imbalanced = fracPass >= 0.80 || fracPass <= 0.20
	return fracPass, imbalanced
}
