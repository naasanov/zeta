package eval

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/naasanov/zsh-autopilot/daemon/internal/protocol"
)

// containsGrader reports whether out contains sub.
func containsGrader(sub string) Grader {
	return GraderFunc{
		N: "contains:" + sub,
		F: func(_ protocol.Request, out string) (bool, error) {
			return strings.Contains(out, sub), nil
		},
	}
}

func basicCase(asserts ...Assertion) Case {
	return Case{ID: "T1", Category: "test", Req: protocol.Request{Kind: protocol.KindTyping, Buf: "git ad"}, Asserts: asserts}
}

// TestRunCase_CapturesProviderTTFT guards the plumbing the report's P50
// LATENCY column depends on: provider.Completion.TTFT must survive into
// Sample.TTFT on the success path, and stay zero on the error path (an
// errored call never got a first byte, so it must not silently contribute a
// fake zero-latency data point to a cell's median).
func TestRunCase_CapturesProviderTTFT(t *testing.T) {
	p := NewStubProvider(
		StubResult{Output: "ok", TTFT: 42 * time.Millisecond},
		StubResult{Err: errBoom},
	)
	r := &Runner{Provider: p, FixedN: 2}
	c := basicCase(Assertion{Label: "x", Polarity: Measure, Grader: containsGrader("o")})

	results := r.Run(context.Background(), []Case{c})
	res := results[0]
	if len(res.Samples) != 2 {
		t.Fatalf("want 2 samples, got %d", len(res.Samples))
	}
	if res.Samples[0].TTFT != 42*time.Millisecond {
		t.Errorf("want successful sample's TTFT = 42ms, got %v", res.Samples[0].TTFT)
	}
	if res.Samples[1].Err == nil || res.Samples[1].TTFT != 0 {
		t.Errorf("want errored sample to carry no TTFT, got Err=%v TTFT=%v", res.Samples[1].Err, res.Samples[1].TTFT)
	}
}

func TestRun_IdenticalOutputsSaturateAtMinRuns(t *testing.T) {
	p := NewStubProvider(StubResult{Output: "d"})
	r := &Runner{Provider: p}
	c := basicCase(Assertion{Label: "has-d", Polarity: Must, Threshold: 0.8, Grader: containsGrader("d")})

	results := r.Run(context.Background(), []Case{c})
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	got := results[0]
	if got.Runs != defaultMinRuns {
		t.Fatalf("want %d runs (saturated), got %d", defaultMinRuns, got.Runs)
	}
	if got.Errors != 0 {
		t.Fatalf("want 0 errors, got %d", got.Errors)
	}
	if !got.Asserts[0].Pass {
		t.Fatalf("want assertion to pass, got %+v", got.Asserts[0])
	}
}

func TestRun_DisagreementEscalatesToMaxRuns(t *testing.T) {
	// Alternates present/absent for the "has-d" grader every other sample,
	// so the first 3 samples cannot possibly all agree.
	p := NewStubProvider(StubResult{Output: "d"}, StubResult{Output: "x"})
	r := &Runner{Provider: p}
	c := basicCase(Assertion{Label: "has-d", Polarity: Measure, Grader: containsGrader("d")})

	results := r.Run(context.Background(), []Case{c})
	got := results[0]
	if got.Runs != defaultMaxRuns {
		t.Fatalf("want %d runs (escalated), got %d", defaultMaxRuns, got.Runs)
	}
}

func TestRun_FixedNOverridesAdaptivity(t *testing.T) {
	// Alternating outputs would normally escalate; FixedN must skip that.
	p := NewStubProvider(StubResult{Output: "d"}, StubResult{Output: "x"})
	r := &Runner{Provider: p, FixedN: 5}
	c := basicCase(Assertion{Label: "has-d", Polarity: Measure, Grader: containsGrader("d")})

	results := r.Run(context.Background(), []Case{c})
	got := results[0]
	if got.Runs != 5 {
		t.Fatalf("want exactly 5 runs, got %d", got.Runs)
	}
}

func TestRun_FixedNClampedToMaxRuns(t *testing.T) {
	p := NewStubProvider(StubResult{Output: "d"})
	r := &Runner{Provider: p, FixedN: 1000, MaxRuns: 10}
	c := basicCase(Assertion{Label: "has-d", Polarity: Measure, Grader: containsGrader("d")})

	results := r.Run(context.Background(), []Case{c})
	got := results[0]
	if got.Runs != 10 {
		t.Fatalf("want FixedN clamped to MaxRuns=10, got %d", got.Runs)
	}
}

func TestRun_ErrorsExcludedFromGradingButCounted(t *testing.T) {
	p := NewStubProvider(
		StubResult{Output: "d"},
		StubResult{Err: errors.New("boom")},
		StubResult{Output: "d"},
	)
	r := &Runner{Provider: p, FixedN: 3}
	c := basicCase(Assertion{Label: "has-d", Polarity: Must, Threshold: 1.0, Grader: containsGrader("d")})

	results := r.Run(context.Background(), []Case{c})
	got := results[0]
	if got.Runs != 2 {
		t.Fatalf("want 2 successful runs, got %d", got.Runs)
	}
	if got.Errors != 1 {
		t.Fatalf("want 1 error, got %d", got.Errors)
	}
	ar := got.Asserts[0]
	if ar.Graded != 2 {
		t.Fatalf("want Graded=2 (errors excluded), got %d", ar.Graded)
	}
	if ar.Present != 2 {
		t.Fatalf("want Present=2, got %d", ar.Present)
	}
	if !ar.Pass {
		t.Fatalf("want must-assertion to pass at 2/2, got %+v", ar)
	}
}

func TestRun_AllErroredNeverPasses(t *testing.T) {
	p := NewStubProvider(StubResult{Err: errors.New("boom")})
	r := &Runner{Provider: p, FixedN: 3}
	c := basicCase(
		Assertion{Label: "must", Polarity: Must, Threshold: 0.0, Grader: containsGrader("d")},
		Assertion{Label: "mustnot", Polarity: MustNot, Threshold: 1.0, Grader: containsGrader("d")},
	)

	results := r.Run(context.Background(), []Case{c})
	got := results[0]
	if got.Errors != 3 || got.Runs != 0 {
		t.Fatalf("want 3 errors 0 runs, got errors=%d runs=%d", got.Errors, got.Runs)
	}
	for _, ar := range got.Asserts {
		if ar.Graded != 0 {
			t.Fatalf("want Graded=0 when every sample errored, got %d", ar.Graded)
		}
		if ar.Pass {
			t.Fatalf("want Graded==0 to never pass (even with a permissive threshold), got %+v", ar)
		}
	}
}

func TestScoring_Must(t *testing.T) {
	cases := []struct {
		name      string
		present   int
		graded    int
		threshold float64
		wantPass  bool
	}{
		{"above threshold", 9, 10, 0.8, true},
		{"at threshold", 8, 10, 0.8, true},
		{"below threshold", 7, 10, 0.8, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pass := tc.graded > 0 && float64(tc.present)/float64(tc.graded) >= tc.threshold
			if pass != tc.wantPass {
				t.Fatalf("Must(%d/%d, thresh=%.2f) = %v, want %v", tc.present, tc.graded, tc.threshold, pass, tc.wantPass)
			}
		})
	}
}

func TestScoring_MustNot(t *testing.T) {
	cases := []struct {
		name      string
		present   int
		graded    int
		threshold float64
		wantPass  bool
	}{
		{"above ceiling", 2, 10, 0.1, false},
		{"at ceiling", 1, 10, 0.1, false}, // 0.1 is NOT < 0.1
		{"below ceiling", 0, 10, 0.1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pass := tc.graded > 0 && float64(tc.present)/float64(tc.graded) < tc.threshold
			if pass != tc.wantPass {
				t.Fatalf("MustNot(%d/%d, thresh=%.2f) = %v, want %v", tc.present, tc.graded, tc.threshold, pass, tc.wantPass)
			}
		})
	}
}

func TestRun_TripWireRecordsOnlyFirstOffending(t *testing.T) {
	p := NewStubProvider(
		StubResult{Output: "ok"},
		StubResult{Output: "&& bad1"},
		StubResult{Output: "&& bad2"},
	)
	r := &Runner{Provider: p, FixedN: 3}
	c := basicCase(Assertion{Label: "no-leading-op", Polarity: TripWire, Grader: containsGrader("&&")})

	results := r.Run(context.Background(), []Case{c})
	ar := results[0].Asserts[0]
	if ar.Pass {
		t.Fatalf("want trip wire to fail once tripped")
	}
	if ar.FirstOffending != "&& bad1" {
		t.Fatalf("want FirstOffending=%q, got %q", "&& bad1", ar.FirstOffending)
	}
}

func TestRun_TripWirePassesWhenNeverPresent(t *testing.T) {
	p := NewStubProvider(StubResult{Output: "ok"})
	r := &Runner{Provider: p, FixedN: 3}
	c := basicCase(Assertion{Label: "no-leading-op", Polarity: TripWire, Grader: containsGrader("&&")})

	results := r.Run(context.Background(), []Case{c})
	ar := results[0].Asserts[0]
	if !ar.Pass {
		t.Fatalf("want trip wire to pass when never present, got %+v", ar)
	}
	if ar.FirstOffending != "" {
		t.Fatalf("want empty FirstOffending, got %q", ar.FirstOffending)
	}
}

func TestRun_Measure_AlwaysPasses(t *testing.T) {
	p := NewStubProvider(StubResult{Output: "x"})
	r := &Runner{Provider: p, FixedN: 3}
	c := basicCase(Assertion{Label: "tracked", Polarity: Measure, Grader: containsGrader("d")})

	results := r.Run(context.Background(), []Case{c})
	if !results[0].Asserts[0].Pass {
		t.Fatalf("Measure assertions must always report Pass=true")
	}
}

func TestRun_MultipleCasesPreserveOrderAndIndependence(t *testing.T) {
	p := NewStubProvider(StubResult{Output: "d"})
	r := &Runner{Provider: p}
	c1 := Case{ID: "A", Category: "cat1", Req: protocol.Request{}, Asserts: []Assertion{
		{Label: "a", Polarity: Must, Threshold: 1.0, Grader: containsGrader("d")},
	}}
	c2 := Case{ID: "B", Category: "cat2", Req: protocol.Request{}, Asserts: []Assertion{
		{Label: "b", Polarity: Must, Threshold: 1.0, Grader: containsGrader("d")},
	}}
	results := r.Run(context.Background(), []Case{c1, c2})
	if len(results) != 2 || results[0].CaseID != "A" || results[1].CaseID != "B" {
		t.Fatalf("want results in input order [A B], got %+v", results)
	}
}

func TestRun_EmptyCasesReturnsEmptyResults(t *testing.T) {
	p := NewStubProvider(StubResult{Output: "d"})
	r := &Runner{Provider: p}
	results := r.Run(context.Background(), nil)
	if len(results) != 0 {
		t.Fatalf("want 0 results for 0 cases, got %d", len(results))
	}
}

func TestRun_ProviderAndModelPopulated(t *testing.T) {
	p := &StubProvider{Script: []StubResult{{Output: "d"}}, PName: "codestral", PModel: "codestral-2601"}
	r := &Runner{Provider: p}
	c := basicCase()
	results := r.Run(context.Background(), []Case{c})
	if results[0].Provider != "codestral" || results[0].Model != "codestral-2601" {
		t.Fatalf("want provider/model populated from Provider, got %+v", results[0])
	}
}

// TestRunCase_GraderErrorsNeverPass guards the hole found reviewing Part 1:
// a Grader that always errors produces zero graded samples, and the natural
// formulation of each polarity would then read as a pass — most dangerously
// for TripWire, whose "no occurrences seen" is indistinguishable from "never
// looked". An assertion that never ran must fail loudly for EVERY polarity,
// and the grader errors must be counted rather than swallowed.
func TestRunCase_GraderErrorsNeverPass(t *testing.T) {
	boom := GraderFunc{N: "boom", F: func(protocol.Request, string) (bool, error) {
		return false, errors.New("grader exploded")
	}}

	for _, pol := range []Polarity{Must, MustNot, TripWire, Measure} {
		t.Run(string(pol), func(t *testing.T) {
			r := &Runner{Provider: NewStubProvider(StubResult{Output: " status"})}
			c := Case{
				ID:      "G1",
				Asserts: []Assertion{{Label: "always errors", Polarity: pol, Threshold: 0.8, Grader: boom}},
			}
			got := r.Run(context.Background(), []Case{c})[0]
			ar := got.Asserts[0]

			if ar.Graded != 0 {
				t.Fatalf("Graded = %d, want 0 (grader always errors)", ar.Graded)
			}
			if ar.GraderErrors == 0 {
				t.Error("GraderErrors = 0, want the grader failures to be counted, not swallowed")
			}
			if ar.Pass {
				t.Errorf("polarity %s passed with 0 graded samples; an assertion that never ran must never pass", pol)
			}
			if got.Errors != 0 {
				t.Errorf("Errors = %d, want 0 — a grader error is not a provider error", got.Errors)
			}
			if ar.FirstGraderError != "grader exploded" {
				t.Errorf("FirstGraderError = %q, want %q — a grader error's message must not be swallowed either", ar.FirstGraderError, "grader exploded")
			}
		})
	}
}

// TestRunCase_FirstGraderErrorKeepsOnlyTheFirst mirrors
// TestRun_TripWireRecordsOnlyFirstOffending: a grader that errors with a
// DIFFERENT message on each call must still surface only the first one, not
// the last or a concatenation — same "keep the first instance" contract.
func TestRunCase_FirstGraderErrorKeepsOnlyTheFirst(t *testing.T) {
	calls := 0
	flaky := GraderFunc{N: "flaky", F: func(protocol.Request, string) (bool, error) {
		calls++
		return false, fmt.Errorf("boom #%d", calls)
	}}
	r := &Runner{Provider: NewStubProvider(StubResult{Output: " status"}), FixedN: 3}
	c := Case{ID: "G2", Asserts: []Assertion{{Label: "flaky", Polarity: Measure, Grader: flaky}}}

	got := r.Run(context.Background(), []Case{c})[0]
	ar := got.Asserts[0]
	if ar.GraderErrors != 3 {
		t.Fatalf("GraderErrors = %d, want 3", ar.GraderErrors)
	}
	if ar.FirstGraderError != "boom #1" {
		t.Errorf("FirstGraderError = %q, want %q (the first error, not the last or all of them)", ar.FirstGraderError, "boom #1")
	}
}

// TestTruncateGraderError checks that a long grader error message is
// truncated, but that the truncated form still contains an identifying
// prefix (in the live bug this fix targets, an HTTP status code and a model
// id sit right at the start of the message).
func TestTruncateGraderError(t *testing.T) {
	short := "404 models/some-judge-model is not found for API version v1main"
	if got := truncateGraderError(short); got != short {
		t.Errorf("short message should pass through unchanged, got %q", got)
	}

	long := short + strings.Repeat("x", 5000)
	got := truncateGraderError(long)
	if len(got) >= len(long) {
		t.Fatalf("want the long message truncated, got len=%d (original len=%d)", len(got), len(long))
	}
	if !strings.Contains(got, "404 models/some-judge-model is not found") {
		t.Errorf("truncated message lost the identifying status code/model id: %q", got)
	}
}

// TestRun_ProgressIsInCaseOrder pins the guarantee live progress output
// depends on: cases run concurrently (defaultConcurrency workers), but
// Progress must fire in CASE order. If it fired in completion order, the Nth
// symbol on the pytest-style row would belong to whichever case happened to
// finish Nth, and reading "the 5th case failed" off the 5th dot would be
// wrong. Run with -race: this is where an out-of-order or racy emission shows.
func TestRun_ProgressIsInCaseOrder(t *testing.T) {
	const n = 50
	cases := make([]Case, n)
	for i := range cases {
		cases[i] = Case{ID: fmt.Sprintf("C%02d", i), Category: "order"}
	}

	var mu sync.Mutex
	var gotIDs []string
	r := &Runner{
		Provider: NewStubProvider(StubResult{Output: " x"}),
		Progress: func(c Case, _ CaseResult) {
			mu.Lock()
			defer mu.Unlock()
			gotIDs = append(gotIDs, c.ID)
		},
	}
	r.Run(context.Background(), cases)

	if len(gotIDs) != n {
		t.Fatalf("Progress fired %d times, want exactly %d (one per case)", len(gotIDs), n)
	}
	for i, id := range gotIDs {
		if want := cases[i].ID; id != want {
			t.Fatalf("Progress[%d] = %s, want %s — emissions must be in case order, not completion order", i, id, want)
		}
	}
}

// TestRun_ProgressResultMatchesReturnedResult guards against the emitted
// CaseResult drifting from the one Run returns — the row would then disagree
// with the scorecard printed underneath it.
func TestRun_ProgressResultMatchesReturnedResult(t *testing.T) {
	cases := []Case{
		{ID: "P1", Asserts: []Assertion{{Label: "has-x", Polarity: Must, Threshold: 0.8, Grader: containsGrader("x")}}},
		{ID: "P2", Asserts: []Assertion{{Label: "has-zzz", Polarity: Must, Threshold: 0.8, Grader: containsGrader("zzz")}}},
	}

	var mu sync.Mutex
	seen := map[string]rune{}
	r := &Runner{
		Provider: NewStubProvider(StubResult{Output: " x"}),
		Progress: func(c Case, res CaseResult) {
			mu.Lock()
			defer mu.Unlock()
			seen[c.ID] = CaseSymbol(res)
		},
	}
	got := r.Run(context.Background(), cases)

	for _, res := range got {
		if want := CaseSymbol(res); seen[res.CaseID] != want {
			t.Errorf("case %s: progress symbol %q != final result symbol %q", res.CaseID, seen[res.CaseID], want)
		}
	}
	if seen["P1"] != '.' {
		t.Errorf("P1 symbol = %q, want '.' (assertion passes)", seen["P1"])
	}
	if seen["P2"] != 'x' {
		t.Errorf("P2 symbol = %q, want 'x' (assertion fails)", seen["P2"])
	}
}

func TestCaseSymbol(t *testing.T) {
	tests := []struct {
		name string
		res  CaseResult
		want rune
	}{
		{"all pass", CaseResult{Runs: 3, Asserts: []AssertionResult{{Pass: true}}}, '.'},
		{"no assertions is still a pass", CaseResult{Runs: 3}, '.'},
		{"a failure", CaseResult{Runs: 3, Asserts: []AssertionResult{{Pass: false}}}, 'x'},
		{
			"a tripped wire outranks an ordinary failure",
			CaseResult{Runs: 3, Asserts: []AssertionResult{{Pass: false}, {Pass: false, Polarity: TripWire}}},
			'!',
		},
		{
			"no successful runs outranks everything",
			CaseResult{Runs: 0, Errors: 3, Asserts: []AssertionResult{{Pass: false, Polarity: TripWire}}},
			'E',
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CaseSymbol(tt.res); got != tt.want {
				t.Errorf("CaseSymbol() = %q, want %q", got, tt.want)
			}
		})
	}
}
