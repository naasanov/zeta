package eval

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func ar(label string, pol Polarity, present, graded int, pass bool) AssertionResult {
	return AssertionResult{Label: label, Polarity: pol, Present: present, Graded: graded, Pass: pass}
}

func arTrip(label string, present, graded int, pass bool, offending string) AssertionResult {
	a := ar(label, TripWire, present, graded, pass)
	a.FirstOffending = offending
	return a
}

func cr(id, category string, asserts ...AssertionResult) CaseResult {
	return CaseResult{CaseID: id, Category: category, Asserts: asserts}
}

func run(meta Meta, results ...CaseResult) Run {
	return Run{Meta: meta, Results: results}
}

var baseMeta = Meta{Provider: "codestral", Model: "codestral-1", Prompt: "default", NPolicy: "adaptive(min=3,max=10)"}

func TestDiffRuns_Identical(t *testing.T) {
	results := []CaseResult{
		cr("E1", "context", ar("has-commit", Must, 8, 10, true)),
	}
	rep := DiffRuns(run(baseMeta, results...), run(baseMeta, results...))

	if len(rep.Rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rep.Rows))
	}
	if rep.Rows[0].Class != RowUnchanged {
		t.Fatalf("want RowUnchanged, got %v", rep.Rows[0].Class)
	}
	if rep.Rows[0].Flip != NoFlip {
		t.Fatalf("want no flip, got %v", rep.Rows[0].Flip)
	}
	if rep.Regressed() {
		t.Fatalf("identical runs must not be Regressed()")
	}
	if len(rep.MetaWarnings) != 0 {
		t.Fatalf("identical meta must not warn, got %v", rep.MetaWarnings)
	}
}

func TestDiffRuns_CleanImprovement(t *testing.T) {
	// Large delta at a solid N: 5/10 -> 9/10 is 40pp, clears the N=10 noise
	// floor (~31.6pp) easily.
	before := []CaseResult{cr("E4", "context", ar("suffix-in-dir", Must, 5, 10, false))}
	after := []CaseResult{cr("E4", "context", ar("suffix-in-dir", Must, 9, 10, true))}

	rep := DiffRuns(run(baseMeta, before...), run(baseMeta, after...))
	row := rep.Rows[0]
	if row.Class != RowImproved {
		t.Fatalf("want RowImproved, got %v (delta=%v floor=%v)", row.Class, row.DeltaPP, row.NoiseFloorPP)
	}
	if row.Flip != FlipFailToPass {
		t.Fatalf("want FlipFailToPass, got %v", row.Flip)
	}
	if rep.Regressed() {
		t.Fatalf("an improvement must not be Regressed()")
	}
}

func TestDiffRuns_CleanRegression(t *testing.T) {
	before := []CaseResult{cr("E4", "context", ar("suffix-in-dir", Must, 9, 10, true))}
	after := []CaseResult{cr("E4", "context", ar("suffix-in-dir", Must, 5, 10, false))}

	rep := DiffRuns(run(baseMeta, before...), run(baseMeta, after...))
	row := rep.Rows[0]
	if row.Class != RowRegressed {
		t.Fatalf("want RowRegressed, got %v", row.Class)
	}
	if row.Flip != FlipPassToFail {
		t.Fatalf("want FlipPassToFail, got %v", row.Flip)
	}
	if !rep.Regressed() {
		t.Fatalf("a status flip to FAIL must set Regressed()")
	}
}

func TestDiffRuns_MustNotDirection(t *testing.T) {
	// For MustNot, a LOWER present-rate is the improvement. Threshold<10%.
	// before: 4/10 present (40%, fails <10% ceiling) -> after: 0/10 (0%, passes).
	before := []CaseResult{cr("B1", "fabrication", ar("no-invented-msg", MustNot, 4, 10, false))}
	after := []CaseResult{cr("B1", "fabrication", ar("no-invented-msg", MustNot, 0, 10, true))}

	rep := DiffRuns(run(baseMeta, before...), run(baseMeta, after...))
	row := rep.Rows[0]
	// This is a status flip (false->true), so it's classified via the flip
	// path, not the noise-floor path — but it must land as "improved" not
	// "regressed" even though the polarity is inverted.
	if row.Class != RowImproved {
		t.Fatalf("want RowImproved for a MustNot rate decrease, got %v", row.Class)
	}
}

func TestDiffRuns_StatusFlipBothDirections(t *testing.T) {
	tests := []struct {
		name                   string
		beforePresent, afterPr int
		beforePass, afterPass  bool
		wantFlip               Flip
		wantClass              RowClass
	}{
		{"fail to pass", 5, 9, false, true, FlipFailToPass, RowImproved},
		{"pass to fail", 9, 5, true, false, FlipPassToFail, RowRegressed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := []CaseResult{cr("E1", "context", ar("x", Must, tc.beforePresent, 10, tc.beforePass))}
			after := []CaseResult{cr("E1", "context", ar("x", Must, tc.afterPr, 10, tc.afterPass))}
			rep := DiffRuns(run(baseMeta, before...), run(baseMeta, after...))
			row := rep.Rows[0]
			if row.Flip != tc.wantFlip {
				t.Fatalf("want flip %v, got %v", tc.wantFlip, row.Flip)
			}
			if row.Class != tc.wantClass {
				t.Fatalf("want class %v, got %v", tc.wantClass, row.Class)
			}
		})
	}
}

func TestDiffRuns_TripWireNewlyTripped(t *testing.T) {
	before := []CaseResult{cr("A1", "syntax", arTrip("no-leading-op", 0, 3, true, ""))}
	after := []CaseResult{cr("A1", "syntax", arTrip("no-leading-op", 1, 3, false, "&& rm -rf /"))}

	rep := DiffRuns(run(baseMeta, before...), run(baseMeta, after...))
	row := rep.Rows[0]
	if !row.TripWireNewlyTripped {
		t.Fatalf("want TripWireNewlyTripped, got row=%+v", row)
	}
	if row.TripWireOffending != "&& rm -rf /" {
		t.Fatalf("want offending output captured, got %q", row.TripWireOffending)
	}
	if row.Class != RowRegressed {
		t.Fatalf("want RowRegressed, got %v", row.Class)
	}
	if !rep.Regressed() {
		t.Fatalf("a newly tripped trip-wire must set Regressed()")
	}

	var buf bytes.Buffer
	if err := rep.Text(&buf); err != nil {
		t.Fatalf("Text: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "TRIP WIRES NEWLY TRIPPED") {
		t.Fatalf("want trip-wire section in text, got:\n%s", out)
	}
	if !strings.Contains(out, `"&& rm -rf /"`) {
		t.Fatalf("want offending output printed verbatim, got:\n%s", out)
	}
}

func TestDiffRuns_TripWireFixed(t *testing.T) {
	before := []CaseResult{cr("A1", "syntax", arTrip("no-leading-op", 1, 3, false, "&& bad"))}
	after := []CaseResult{cr("A1", "syntax", arTrip("no-leading-op", 0, 3, true, ""))}

	rep := DiffRuns(run(baseMeta, before...), run(baseMeta, after...))
	row := rep.Rows[0]
	if row.TripWireNewlyTripped {
		t.Fatalf("a fix is not a new trip")
	}
	if row.Class != RowImproved {
		t.Fatalf("want RowImproved, got %v", row.Class)
	}
	if row.Flip != FlipFailToPass {
		t.Fatalf("want FlipFailToPass, got %v", row.Flip)
	}
	if rep.Regressed() {
		t.Fatalf("a trip-wire fix must not be Regressed()")
	}
}

func TestDiffRuns_NewAndRemovedCases(t *testing.T) {
	before := []CaseResult{
		cr("E1", "context", ar("has-commit", Must, 8, 10, true)),
		cr("E9", "context", ar("gone", Must, 8, 10, true)), // removed in after
	}
	after := []CaseResult{
		cr("E1", "context", ar("has-commit", Must, 8, 10, true)),
		cr("E10", "context", ar("fresh", Must, 8, 10, true)), // new in after
	}

	rep := DiffRuns(run(baseMeta, before...), run(baseMeta, after...))

	var gotNew, gotRemoved bool
	for _, row := range rep.Rows {
		if row.CaseID == "E10" {
			if row.Class != RowNew {
				t.Fatalf("E10 want RowNew, got %v", row.Class)
			}
			if row.Before != nil {
				t.Fatalf("E10 want Before nil, got %+v", row.Before)
			}
			gotNew = true
		}
		if row.CaseID == "E9" {
			if row.Class != RowRemoved {
				t.Fatalf("E9 want RowRemoved, got %v", row.Class)
			}
			if row.After != nil {
				t.Fatalf("E9 want After nil, got %+v", row.After)
			}
			gotRemoved = true
		}
	}
	if !gotNew || !gotRemoved {
		t.Fatalf("want both a new and a removed row, got rows=%+v", rep.Rows)
	}
	if rep.Regressed() {
		t.Fatalf("adding/removing cases alone must not be Regressed()")
	}
}

func TestDiffRuns_GradedZeroEitherSideIsNotComparable(t *testing.T) {
	tests := []struct {
		name          string
		before, after AssertionResult
	}{
		{"zero before", ar("x", Must, 0, 0, false), ar("x", Must, 8, 10, true)},
		{"zero after", ar("x", Must, 8, 10, true), ar("x", Must, 0, 0, false)},
		{"zero both", ar("x", Must, 0, 0, false), ar("x", Must, 0, 0, false)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := []CaseResult{cr("E1", "context", tc.before)}
			after := []CaseResult{cr("E1", "context", tc.after)}
			rep := DiffRuns(run(baseMeta, before...), run(baseMeta, after...))
			row := rep.Rows[0]
			if row.Class != RowNotComparable {
				t.Fatalf("want RowNotComparable, got %v", row.Class)
			}
			if row.Flip != NoFlip {
				t.Fatalf("a Graded==0 side must never read as a flip, got %v", row.Flip)
			}
			if rep.Regressed() {
				t.Fatalf("not-comparable rows alone must not be Regressed()")
			}
		})
	}
}

func TestDiffRuns_MetaDiffers(t *testing.T) {
	results := []CaseResult{cr("E1", "context", ar("has-commit", Must, 8, 10, true))}
	beforeMeta := baseMeta
	afterMeta := baseMeta
	afterMeta.Provider = "anthropic"
	afterMeta.Model = "haiku-4.5"

	rep := DiffRuns(run(beforeMeta, results...), run(afterMeta, results...))
	if len(rep.MetaWarnings) != 2 {
		t.Fatalf("want 2 warnings (provider+model), got %v", rep.MetaWarnings)
	}

	var buf bytes.Buffer
	if err := rep.Text(&buf); err != nil {
		t.Fatalf("Text: %v", err)
	}
	if !strings.Contains(buf.String(), "WARNING") {
		t.Fatalf("want WARNING in header for differing meta, got:\n%s", buf.String())
	}
}

func TestDiffRuns_NoiseFloorSuppressesSmallDeltaAtSmallN(t *testing.T) {
	// The plan doc's own example: 7/10 -> 8/10 (10pp) is noise, not a change.
	before := []CaseResult{cr("E1", "context", ar("has-commit", Must, 7, 10, true))}
	after := []CaseResult{cr("E1", "context", ar("has-commit", Must, 8, 10, true))}

	rep := DiffRuns(run(baseMeta, before...), run(baseMeta, after...))
	row := rep.Rows[0]
	if row.Class != RowUnchanged {
		t.Fatalf("want RowUnchanged (below noise floor), got %v (delta=%v floor=%v)", row.Class, row.DeltaPP, row.NoiseFloorPP)
	}
	if !row.BelowNoiseFloor {
		t.Fatalf("want BelowNoiseFloor flagged")
	}

	var buf bytes.Buffer
	if err := rep.Text(&buf); err != nil {
		t.Fatalf("Text: %v", err)
	}
	if !strings.Contains(buf.String(), "noise floor") {
		t.Fatalf("want a visible callout that this was suppressed by the noise floor, got:\n%s", buf.String())
	}
	if rep.Regressed() {
		t.Fatalf("a noise-floor-suppressed wobble must not be Regressed()")
	}
}

func TestDiffRuns_StatusFlipAtSmallNStillReported(t *testing.T) {
	// Same tiny N as the noise-floor test above, but this time Pass itself
	// flips (crossing the assertion's own threshold) — that must be reported
	// as a flip regardless of how small the underlying N is, because flips
	// are categorical, not statistical.
	before := []CaseResult{cr("E1", "context", ar("has-commit", Must, 2, 3, false))} // 2/3=67%, below 80% threshold -> fail
	after := []CaseResult{cr("E1", "context", ar("has-commit", Must, 3, 3, true))}   // 3/3=100% -> pass

	rep := DiffRuns(run(baseMeta, before...), run(baseMeta, after...))
	row := rep.Rows[0]
	if row.Flip != FlipFailToPass {
		t.Fatalf("want FlipFailToPass even at N=3, got %v", row.Flip)
	}
	if row.Class != RowImproved {
		t.Fatalf("want RowImproved, got %v", row.Class)
	}
}

func TestDiffRuns_SuiteAggregateArithmetic(t *testing.T) {
	before := []CaseResult{
		cr("A1", "syntax", ar("a", Must, 8, 10, true), ar("b", Must, 2, 10, false)),
		cr("B1", "fab", ar("c", MustNot, 1, 10, true)),
	}
	after := []CaseResult{
		cr("A1", "syntax", ar("a", Must, 9, 10, true), ar("b", Must, 8, 10, true)),
		cr("B1", "fab", ar("c", MustNot, 1, 10, true)),
	}

	rep := DiffRuns(run(baseMeta, before...), run(baseMeta, after...))

	if rep.Suite.BeforeTotal != 3 || rep.Suite.BeforePassed != 2 {
		t.Fatalf("before suite totals wrong: %+v", rep.Suite)
	}
	if rep.Suite.AfterTotal != 3 || rep.Suite.AfterPassed != 3 {
		t.Fatalf("after suite totals wrong: %+v", rep.Suite)
	}
	wantDelta := 100.0 * (1.0 - 2.0/3.0)
	if diff := rep.Suite.DeltaPP - wantDelta; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("want suite delta %.4f, got %.4f", wantDelta, rep.Suite.DeltaPP)
	}

	// Per-category aggregate for "syntax": before 1/2 passed, after 2/2.
	var syntaxCat *CategoryAggregate
	for i := range rep.Categories {
		if rep.Categories[i].Category == "syntax" {
			syntaxCat = &rep.Categories[i]
		}
	}
	if syntaxCat == nil {
		t.Fatalf("want a syntax category aggregate, got %+v", rep.Categories)
	}
	if syntaxCat.BeforePassed != 1 || syntaxCat.BeforeTotal != 2 || syntaxCat.AfterPassed != 2 || syntaxCat.AfterTotal != 2 {
		t.Fatalf("syntax category totals wrong: %+v", syntaxCat)
	}
}

func TestDiffRuns_MeasureExcludedFromAggregateAndNeverAsserted(t *testing.T) {
	before := []CaseResult{cr("D1", "loop", ar("repeat", Measure, 3, 10, true))}
	after := []CaseResult{cr("D1", "loop", ar("repeat", Measure, 9, 10, true))}

	rep := DiffRuns(run(baseMeta, before...), run(baseMeta, after...))
	if rep.Suite.BeforeTotal != 0 || rep.Suite.AfterTotal != 0 {
		t.Fatalf("Measure assertions must not count toward the suite aggregate, got %+v", rep.Suite)
	}
	row := rep.Rows[0]
	if row.Class != RowMeasure {
		t.Fatalf("want RowMeasure, got %v", row.Class)
	}
	if rep.Regressed() {
		t.Fatalf("a Measure delta must never contribute to Regressed()")
	}
}

func TestDiffRuns_RegressedFalseCases(t *testing.T) {
	tests := []struct {
		name          string
		before, after CaseResult
	}{
		{"identical", cr("E1", "context", ar("x", Must, 8, 10, true)), cr("E1", "context", ar("x", Must, 8, 10, true))},
		{"improvement", cr("E1", "context", ar("x", Must, 5, 10, false)), cr("E1", "context", ar("x", Must, 9, 10, true))},
		{"tiny wobble", cr("E1", "context", ar("x", Must, 7, 10, true)), cr("E1", "context", ar("x", Must, 8, 10, true))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rep := DiffRuns(run(baseMeta, tc.before), run(baseMeta, tc.after))
			if rep.Regressed() {
				t.Fatalf("want Regressed()==false")
			}
		})
	}
}

func TestDiffRuns_RegressedTrueCases(t *testing.T) {
	tests := []struct {
		name          string
		before, after CaseResult
	}{
		{"status flip to fail", cr("E1", "context", ar("x", Must, 9, 10, true)), cr("E1", "context", ar("x", Must, 5, 10, false))},
		{"tripwire newly tripped", cr("A1", "syntax", arTrip("x", 0, 3, true, "")), cr("A1", "syntax", arTrip("x", 1, 3, false, "&& bad"))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rep := DiffRuns(run(baseMeta, tc.before), run(baseMeta, tc.after))
			if !rep.Regressed() {
				t.Fatalf("want Regressed()==true")
			}
		})
	}
}

func TestDiffRuns_SuiteLevelMovementBeyondNoiseFloorRegresses(t *testing.T) {
	// No single row flips Pass (each assertion individually stays within its
	// own per-row noise floor at N=10), but pooled across enough assertions
	// the SUITE pass rate moves by more than its own (much larger-N) floor.
	// Suite N here is 20, floor = 200*sqrt(0.25/20) =~ 22.4pp.
	var before, after []CaseResult
	for i := 0; i < 10; i++ {
		before = append(before, cr("case", "cat", ar("a", Must, 9, 10, true)))
		after = append(after, cr("case", "cat", ar("a", Must, 9, 10, true)))
	}
	for i := 0; i < 10; i++ {
		before = append(before, cr("case2", "cat", ar("b", Must, 9, 10, true)))
		after = append(after, cr("case2", "cat", ar("b", Must, 1, 10, false)))
	}
	rep := DiffRuns(run(baseMeta, before...), run(baseMeta, after...))
	// This will also trip a per-row status flip (case2/b goes pass->fail),
	// which is sufficient by itself — confirms Regressed() catches it via
	// either path.
	if !rep.Regressed() {
		t.Fatalf("want Regressed()==true for a real suite-level drop")
	}
}

func TestLoadRun_RoundTripsWithJSON(t *testing.T) {
	meta := Meta{Provider: "codestral", Model: "codestral-1", Prompt: "default", NPolicy: "fixed=5", Timestamp: time.Now().Truncate(time.Second)}
	results := []CaseResult{
		cr("A1", "syntax", arTrip("no-leading-op", 1, 3, false, "&& bad")),
		cr("E1", "context", ar("has-commit", Must, 8, 10, true)),
	}

	var buf bytes.Buffer
	if err := JSON(&buf, results, meta); err != nil {
		t.Fatalf("JSON: %v", err)
	}

	got, err := LoadRun(&buf)
	if err != nil {
		t.Fatalf("LoadRun: %v", err)
	}
	if got.Meta.Provider != meta.Provider || got.Meta.Model != meta.Model || got.Meta.NPolicy != meta.NPolicy {
		t.Fatalf("meta mismatch: got %+v, want %+v", got.Meta, meta)
	}
	if !got.Meta.Timestamp.Equal(meta.Timestamp) {
		t.Fatalf("timestamp mismatch: got %v, want %v", got.Meta.Timestamp, meta.Timestamp)
	}
	if len(got.Results) != 2 {
		t.Fatalf("want 2 results, got %d", len(got.Results))
	}
	if got.Results[0].CaseID != "A1" || got.Results[0].Asserts[0].FirstOffending != "&& bad" {
		t.Fatalf("round-tripped result mismatch: %+v", got.Results[0])
	}

	// And LoadRun's output must be usable directly by DiffRuns (the whole
	// point of the round trip) — diffing a run against itself.
	rep := DiffRuns(got, got)
	if rep.Regressed() {
		t.Fatalf("a run diffed against its own round-trip must not be Regressed()")
	}
}

func TestLoadRun_CorruptJSONReturnsError(t *testing.T) {
	_, err := LoadRun(strings.NewReader("{ not valid json"))
	if err == nil {
		t.Fatalf("want an error for corrupt JSON")
	}
}

func TestLoadRun_EmptyInputReturnsError(t *testing.T) {
	_, err := LoadRun(strings.NewReader(""))
	if err == nil {
		t.Fatalf("want an error for empty input")
	}
}

func TestDiffReport_TextRendersSampleFixture(t *testing.T) {
	before := []CaseResult{
		cr("A1", "syntax", arTrip("no-leading-op", 0, 3, true, "")),
		cr("E1", "context", ar("has-commit", Must, 7, 10, true)),
		cr("E9", "context", ar("gone-next-run", Must, 8, 10, true)),
	}
	after := []CaseResult{
		cr("A1", "syntax", arTrip("no-leading-op", 1, 3, false, "&& rm -rf /")),
		cr("E1", "context", ar("has-commit", Must, 8, 10, true)),
		cr("E10", "context", ar("fresh-this-run", Must, 9, 10, true)),
	}
	beforeMeta := baseMeta
	afterMeta := baseMeta
	afterMeta.Model = "codestral-2"

	rep := DiffRuns(run(beforeMeta, before...), run(afterMeta, after...))

	var buf bytes.Buffer
	if err := rep.Text(&buf); err != nil {
		t.Fatalf("Text: %v", err)
	}
	t.Logf("sample rendered diff:\n%s", buf.String())

	if !rep.Regressed() {
		t.Fatalf("want Regressed()==true (trip-wire newly tripped)")
	}
}
