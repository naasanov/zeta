package eval

import (
	"context"
	"errors"
	"math"
	"os"
	"strings"
	"testing"
)

// ---- LoadLabels -------------------------------------------------------------

const validLabelsFixture = `
# a comment line, should be skipped

{"case_id":"C3","suggestion":"git push origin v0.1.7","verdict":"fail","note":"mechanical version bump"}
{"case_id":"E3","suggestion":"go test ./...","verdict":"pass","note":"responds to the failed build"}
`

func TestLoadLabels_Valid(t *testing.T) {
	labels, err := LoadLabels(strings.NewReader(validLabelsFixture), Cases())
	if err != nil {
		t.Fatalf("LoadLabels: unexpected error: %v", err)
	}
	if len(labels) != 2 {
		t.Fatalf("got %d labels, want 2", len(labels))
	}
	if labels[0].CaseID != "C3" || labels[0].Suggestion != "git push origin v0.1.7" || labels[0].Verdict != "fail" {
		t.Errorf("labels[0] = %+v, unexpected", labels[0])
	}
	if labels[1].CaseID != "E3" || labels[1].Verdict != "pass" {
		t.Errorf("labels[1] = %+v, unexpected", labels[1])
	}
}

func TestLoadLabels_BlankAndCommentLinesSkipped(t *testing.T) {
	src := "\n\n# comment\n" +
		`{"case_id":"C3","suggestion":"x","verdict":"pass","note":""}` + "\n" +
		"# another comment\n\n"
	labels, err := LoadLabels(strings.NewReader(src), Cases())
	if err != nil {
		t.Fatalf("LoadLabels: unexpected error: %v", err)
	}
	if len(labels) != 1 {
		t.Fatalf("got %d labels, want 1 (comments/blanks should be skipped)", len(labels))
	}
}

// TestLoadLabels_PinnedFile_C3EmptyIsPass loads the pinned judge_labels.jsonl
// and checks the C3 empty-suggestion entry is labeled "pass": abstention is
// intended behaviour per prompt.systemPrompt, and c3Rubric states so
// explicitly — guards that from silently reverting.
func TestLoadLabels_PinnedFile_C3EmptyIsPass(t *testing.T) {
	f, err := os.Open("testdata/judge_labels.jsonl")
	if err != nil {
		t.Fatalf("opening pinned judge_labels.jsonl: %v", err)
	}
	defer f.Close()

	labels, err := LoadLabels(f, Cases())
	if err != nil {
		t.Fatalf("LoadLabels: unexpected error loading the pinned file: %v", err)
	}
	if len(labels) == 0 {
		t.Fatal("pinned judge_labels.jsonl produced zero labels")
	}

	found := false
	for _, l := range labels {
		if l.CaseID == "C3" && l.Suggestion == "" {
			found = true
			if l.Verdict != "pass" {
				t.Errorf("C3 empty-suggestion label verdict = %q, want %q (abstention is intended behaviour per prompt.systemPrompt)", l.Verdict, "pass")
			}
		}
	}
	if !found {
		t.Fatal("pinned judge_labels.jsonl no longer has a C3 empty-suggestion entry")
	}
}

func TestLoadLabels_UnknownCaseID(t *testing.T) {
	src := `{"case_id":"Z99","suggestion":"x","verdict":"pass","note":""}`
	_, err := LoadLabels(strings.NewReader(src), Cases())
	if err == nil {
		t.Fatal("want an error for an unknown case_id, got nil")
	}
	if !strings.Contains(err.Error(), "Z99") {
		t.Errorf("error should name the offending case_id, got: %v", err)
	}
}

func TestLoadLabels_NonJudgedCaseID(t *testing.T) {
	// A1 is a real case, but it's deterministically graded, not judged — a
	// label against it is a labeling mistake, not something that should
	// silently score nothing.
	src := `{"case_id":"A1","suggestion":"x","verdict":"pass","note":""}`
	_, err := LoadLabels(strings.NewReader(src), Cases())
	if err == nil {
		t.Fatal("want an error for a non-judged case_id, got nil")
	}
	if !strings.Contains(err.Error(), "A1") {
		t.Errorf("error should name the offending case_id, got: %v", err)
	}
}

func TestLoadLabels_BadVerdict(t *testing.T) {
	src := `{"case_id":"C3","suggestion":"x","verdict":"maybe","note":""}`
	_, err := LoadLabels(strings.NewReader(src), Cases())
	if err == nil {
		t.Fatal("want an error for an invalid verdict value, got nil")
	}
}

func TestLoadLabels_MalformedJSON(t *testing.T) {
	src := `{"case_id":"C3","suggestion":`
	_, err := LoadLabels(strings.NewReader(src), Cases())
	if err == nil {
		t.Fatal("want an error for malformed JSON, got nil")
	}
}

func TestLoadLabels_UnknownFieldRejected(t *testing.T) {
	src := `{"case_id":"C3","suggestion":"x","verdict":"pass","note":"","extra":"nope"}`
	_, err := LoadLabels(strings.NewReader(src), Cases())
	if err == nil {
		t.Fatal("want an error for an unrecognized field, got nil")
	}
}

// ---- Cohen's kappa ------------------------------------------------------------

func TestCohenKappa_HandComputed(t *testing.T) {
	// 10 labels, 5 pass / 5 fail (balanced). Judge agrees on 8/10:
	// aa=4 (both pass), bb=1 (human pass, judge fail), cc=1 (human fail,
	// judge pass), dd=4 (both fail).
	// po = 8/10 = 0.8
	// pe = (5*5 + 5*5) / 100 = 0.5
	// kappa = (0.8-0.5)/(1-0.5) = 0.6
	got := cohenKappa(4, 1, 1, 4)
	want := 0.6
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("cohenKappa(4,1,1,4) = %v, want %v", got, want)
	}
}

func TestCohenKappa_PerfectAgreement(t *testing.T) {
	got := cohenKappa(5, 0, 0, 5)
	if math.Abs(got-1.0) > 1e-9 {
		t.Errorf("cohenKappa with perfect agreement = %v, want 1.0", got)
	}
}

func TestCohenKappa_LopsidedLabelsAgreementHighKappaLow(t *testing.T) {
	// 30 labels, 27 fail / 3 pass. A judge that ALWAYS says "fail":
	// aa=0 (no true pass agreed), bb=3 (human pass, judge fail, all 3),
	// cc=0, dd=27 (human fail, judge fail, all 27).
	// po = 27/30 = 0.9 (looks great)
	// pe = (3*0 + 27*30) / 900 = 810/900 = 0.9 (all of it is chance, because
	//      the judge's marginal is degenerate — it never varies)
	// kappa = (0.9-0.9)/(1-0.9) = 0
	po := 27.0 / 30.0
	kappa := cohenKappa(0, 3, 0, 27)
	if po < 0.85 {
		t.Fatalf("test setup: expected a high raw agreement fixture, got po=%v", po)
	}
	if math.Abs(kappa) > 1e-9 {
		t.Errorf("cohenKappa(0,3,0,27) = %v, want ~0 — raw agreement (0.9) is inflated by chance, kappa must catch that", kappa)
	}
}

func TestCohenKappa_EmptyIsZero(t *testing.T) {
	if got := cohenKappa(0, 0, 0, 0); got != 0 {
		t.Errorf("cohenKappa(0,0,0,0) = %v, want 0", got)
	}
}

// ---- LabelImbalance -----------------------------------------------------------

func TestLabelImbalance_Lopsided(t *testing.T) {
	var labels []Label
	for i := 0; i < 27; i++ {
		labels = append(labels, Label{CaseID: "C3", Verdict: "fail"})
	}
	for i := 0; i < 3; i++ {
		labels = append(labels, Label{CaseID: "C3", Verdict: "pass"})
	}
	frac, imbalanced := LabelImbalance(labels)
	if !imbalanced {
		t.Error("27/30 fail should be reported as imbalanced")
	}
	if math.Abs(frac-0.1) > 1e-9 {
		t.Errorf("fracPass = %v, want 0.1", frac)
	}
}

func TestLabelImbalance_Balanced(t *testing.T) {
	labels := []Label{
		{CaseID: "C3", Verdict: "pass"},
		{CaseID: "C3", Verdict: "fail"},
	}
	if _, imbalanced := LabelImbalance(labels); imbalanced {
		t.Error("50/50 should not be reported as imbalanced")
	}
}

// ---- ValidateJudges: scriptedJudge, agreement/errors/disagreements ----------

// scriptedJudge is a Judge test double that returns pre-scripted results in
// call order (unlike judge_test.go's fakeJudge, which returns the same
// verdict/err every time) — needed here because ValidateJudges tests need
// different verdicts across different labels processed in one run.
type scriptedJudge struct {
	name    string
	results []scriptedResult
	calls   int
}

type scriptedResult struct {
	verdict JudgeVerdict
	err     error
}

func (s *scriptedJudge) Name() string { return s.name }

func (s *scriptedJudge) Judge(_ context.Context, _ JudgeInput) (JudgeVerdict, error) {
	if s.calls >= len(s.results) {
		return JudgeVerdict{}, errors.New("scriptedJudge: ran out of scripted results")
	}
	r := s.results[s.calls]
	s.calls++
	return r.verdict, r.err
}

func judgedCases(t *testing.T) []Case {
	t.Helper()
	// A configured (non-empty API key) judge grader is what gives
	// judgedAssertion a real, non-empty rubric string to hand to the
	// scriptedJudge's JudgeInput — matches how -judge-validate's real flow
	// resolves Cases() only after confirming a key is set.
	t.Setenv("ZSH_AUTOPILOT_EVAL_JUDGE_KEY", "test-key-for-validate-tests")
	return Cases()
}

func TestValidateJudges_AgreementAndDisagreements(t *testing.T) {
	cases := judgedCases(t)

	labels := []Label{
		{CaseID: "C3", Suggestion: "git push origin v0.1.7", Verdict: "fail"},
		{CaseID: "C3", Suggestion: "git log --oneline", Verdict: "pass"},
		{CaseID: "E3", Suggestion: "go test ./...", Verdict: "pass"},
	}

	j := &scriptedJudge{
		name: "test-judge",
		results: []scriptedResult{
			{verdict: JudgeVerdict{Pass: false, Reason: "mechanical bump"}},    // agrees (fail==fail)
			{verdict: JudgeVerdict{Pass: false, Reason: "disagreeing reason"}}, // disagrees (human said pass)
			{verdict: JudgeVerdict{Pass: true, Reason: "responds to failure"}}, // agrees
		},
	}

	scores := ValidateJudges(context.Background(), labels, cases, []Judge{j})
	if len(scores) != 1 {
		t.Fatalf("got %d scores, want 1", len(scores))
	}
	s := scores[0]

	if s.Total != 3 {
		t.Errorf("Total = %d, want 3", s.Total)
	}
	if s.Agreed != 2 {
		t.Errorf("Agreed = %d, want 2", s.Agreed)
	}
	if math.Abs(s.Agreement-2.0/3.0) > 1e-9 {
		t.Errorf("Agreement = %v, want 2/3", s.Agreement)
	}
	if s.Errors != 0 {
		t.Errorf("Errors = %d, want 0", s.Errors)
	}
	if len(s.Disagreements) != 1 {
		t.Fatalf("got %d disagreements, want 1", len(s.Disagreements))
	}
	d := s.Disagreements[0]
	if d.CaseID != "C3" || d.Suggestion != "git log --oneline" || d.HumanVerdict != "pass" || d.JudgeVerdict != "fail" {
		t.Errorf("disagreement = %+v, unexpected", d)
	}
	if d.JudgeReason != "disagreeing reason" {
		t.Errorf("disagreement reason = %q, want the judge's stated reason", d.JudgeReason)
	}
}

func TestValidateJudges_ErrorsNotCountedAsDisagreements(t *testing.T) {
	cases := judgedCases(t)

	labels := []Label{
		{CaseID: "C3", Suggestion: "a", Verdict: "pass"},
		{CaseID: "C3", Suggestion: "b", Verdict: "fail"},
		{CaseID: "C3", Suggestion: "c", Verdict: "pass"},
		{CaseID: "C3", Suggestion: "d", Verdict: "fail"},
	}

	boom := errors.New("boom")
	j := &scriptedJudge{
		name: "flaky-judge",
		results: []scriptedResult{
			{err: boom},
			{err: boom},
			{verdict: JudgeVerdict{Pass: true, Reason: "ok"}},
			{verdict: JudgeVerdict{Pass: false, Reason: "ok"}},
		},
	}

	scores := ValidateJudges(context.Background(), labels, cases, []Judge{j})
	s := scores[0]

	if s.Errors != 2 {
		t.Errorf("Errors = %d, want 2", s.Errors)
	}
	if s.Total != 2 {
		t.Errorf("Total = %d, want 2 (errored samples must be excluded from Total)", s.Total)
	}
	if s.Agreed != 2 {
		t.Errorf("Agreed = %d, want 2", s.Agreed)
	}
	if s.Agreement != 1.0 {
		t.Errorf("Agreement = %v, want 1.0 — a judge erroring on half the samples must not look like it disagreed on them", s.Agreement)
	}
	if len(s.Disagreements) != 0 {
		t.Errorf("got %d disagreements, want 0", len(s.Disagreements))
	}
}

func TestValidateJudges_UsesProductionRubric(t *testing.T) {
	// The JudgeInput handed to the judge must carry the SAME rubric text
	// cases.go wired into C3's grader — not a re-typed copy that could
	// drift. Assert this by capturing what the judge actually received.
	cases := judgedCases(t)

	var gotRubric string
	rec := &recordingJudge{
		onJudge: func(in JudgeInput) (JudgeVerdict, error) {
			gotRubric = in.Rubric
			return JudgeVerdict{Pass: true, Reason: "ok"}, nil
		},
	}

	labels := []Label{{CaseID: "C3", Suggestion: "x", Verdict: "pass"}}
	ValidateJudges(context.Background(), labels, cases, []Judge{rec})

	_, wantRubric, ok := judgedAssertion(mustCase(t, cases, "C3"))
	if !ok {
		t.Fatal("C3 should be a judged case")
	}
	if gotRubric != wantRubric {
		t.Errorf("judge received rubric %q, want the production rubric %q", gotRubric, wantRubric)
	}
	if gotRubric == "" {
		t.Error("production rubric should not be empty in this test (env key was set)")
	}
}

// recordingJudge is a minimal Judge whose Judge method delegates to onJudge,
// used only to capture the JudgeInput ValidateJudges actually built.
type recordingJudge struct {
	onJudge func(JudgeInput) (JudgeVerdict, error)
}

func (r *recordingJudge) Name() string { return "recording-judge" }
func (r *recordingJudge) Judge(_ context.Context, in JudgeInput) (JudgeVerdict, error) {
	return r.onJudge(in)
}

func mustCase(t *testing.T, cases []Case, id string) Case {
	t.Helper()
	for _, c := range cases {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("case %s not found", id)
	return Case{}
}

// ---- RankByAgreementPerDollar -------------------------------------------------

func TestRankByAgreementPerDollar_Orders(t *testing.T) {
	scores := []JudgeScore{
		{Judge: "expensive-perfect", Agreement: 1.0, CostUSD: 1.0}, // ratio 1.0
		{Judge: "cheap-good", Agreement: 0.9, CostUSD: 0.1},        // ratio 9.0
		{Judge: "free-worthless", Agreement: 0, CostUSD: 0},        // ratio 0
		{Judge: "free-decent", Agreement: 0.5, CostUSD: 0},         // ratio +Inf
	}

	ranked := RankByAgreementPerDollar(scores)
	if len(ranked) != 4 {
		t.Fatalf("got %d ranked scores, want 4", len(ranked))
	}

	wantOrder := []string{"free-decent", "cheap-good", "expensive-perfect", "free-worthless"}
	for i, want := range wantOrder {
		if ranked[i].Judge != want {
			t.Errorf("ranked[%d].Judge = %q, want %q (full order: %v)", i, ranked[i].Judge, want, judgeNames(ranked))
		}
	}
}

func TestRankByAgreementPerDollar_DoesNotMutateInput(t *testing.T) {
	scores := []JudgeScore{
		{Judge: "b", Agreement: 0.5, CostUSD: 1},
		{Judge: "a", Agreement: 0.9, CostUSD: 1},
	}
	_ = RankByAgreementPerDollar(scores)
	if scores[0].Judge != "b" || scores[1].Judge != "a" {
		t.Errorf("RankByAgreementPerDollar mutated its input slice: %v", judgeNames(scores))
	}
}

func judgeNames(scores []JudgeScore) []string {
	names := make([]string, len(scores))
	for i, s := range scores {
		names[i] = s.Judge
	}
	return names
}
