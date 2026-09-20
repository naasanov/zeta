// validate.go is the judge-validation harness: judges are scored against
// hand-written labels.
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

// DefaultJudgeLabelsPath is relative to the daemon module root.
const DefaultJudgeLabelsPath = "internal/eval/testdata/judge_labels.jsonl"

// ---- Label file -------------------------------------------------------------

// Label is one hand-written human judgement.
type Label struct {
	CaseID     string
	Suggestion string
	Verdict    string // "pass" or "fail", the human's call
	Note       string // free text, human-readable only, never interpreted
}

type labelLine struct {
	CaseID     string `json:"case_id"`
	Suggestion string `json:"suggestion"`
	Verdict    string `json:"verdict"`
	Note       string `json:"note"`
}

// LoadLabels validates each row eagerly; a bad row is a fatal error naming
// the line, never a skip-and-warn. Blank lines and "#" lines are skipped.
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
			return nil, fmt.Errorf("eval: judge labels line %d: case_id %q exists but is not a judge-graded case; only judge-graded cases take hand labels", lineNo, raw.CaseID)
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

func judgedAssertion(c Case) (label, rubric string, ok bool) {
	for _, a := range c.Asserts {
		if jg, isJudge := a.Grader.(*judgeGrader); isJudge {
			return jg.label, jg.rubric, true
		}
	}
	return "", "", false
}

// ---- Validation ---------------------------------------------------------

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
	Model string // same as Judge; kept as its own field for report code

	Agreed int // judge verdict == human verdict
	Total  int // labels the judge actually produced a verdict for (excludes Errors)

	Agreement float64 // Agreed/Total; 0 if Total==0
	Kappa     float64 // Cohen's kappa vs the human labels, over the same Total

	// Errors counts failed judge calls (network, malformed response, ctx
	// cancellation); must never be folded into Total/Agreement.
	Errors int

	// FirstError is the first judge-call error's message, truncated.
	FirstError string

	InputTokens  int
	OutputTokens int
	CostUSD      float64 // price-table estimate, not a billing figure

	Disagreements []Disagreement
}

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

// cohenKappa corrects raw agreement for chance: a judge that always says
// "fail" on a lopsided set scores high raw agreement, but kappa ~0 exposes it.
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

// estimateTokens is a rough chars/4 heuristic, not an exact token count.
func estimateTokens(s string) int {
	if s == "" {
		return 0
	}
	return (len(s) + 3) / 4
}

// judgeModelPrice is USD per million tokens, input and output.
type judgeModelPrice struct {
	InPerM  float64
	OutPerM float64
}

// judgeInputPriceTable is directional, vendor-sourced pricing; re-check
// before quoting a budget. An unknown model id prices at 0.
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

// Zero-cost only ranks infinitely good when Agreement > 0, so a judge that
// never agreed doesn't sort to the top.
func agreementPerDollar(s JudgeScore) float64 {
	if s.CostUSD <= 0 {
		if s.Agreement > 0 {
			return math.Inf(1)
		}
		return 0
	}
	return s.Agreement / s.CostUSD
}

// Ties break by higher raw Agreement, then by Judge name, for a
// deterministic order.
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

// LabelImbalance flags a lopsided label set, where raw Agreement is
// misleading and Kappa should be read instead.
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
