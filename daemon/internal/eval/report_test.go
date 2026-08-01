package eval

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// TestMain forces NO_COLOR for this package's tests: report_test.go asserts
// on exact plain-text substrings (e.g. ". 80%"), and letting the ambient
// environment decide whether Text() emits ANSI codes would make those
// assertions flaky depending on where `go test` runs.
func TestMain(m *testing.M) {
	os.Setenv("NO_COLOR", "1")
	os.Exit(m.Run())
}

func sampleResults() []CaseResult {
	return []CaseResult{
		{
			CaseID: "A1", Category: "syntax", Provider: "codestral", Model: "codestral-1", Variant: "prompt.Build",
			Runs: 3, Errors: 0,
			Samples: []Sample{{Output: "ok"}, {Output: "&& bad"}, {Output: "ok"}},
			Asserts: []AssertionResult{
				{Label: "no-leading-op", Polarity: TripWire, Present: 1, Graded: 3, Pass: false, FirstOffending: "&& bad"},
			},
		},
		{
			CaseID: "E1", Category: "context", Provider: "codestral", Model: "codestral-1", Variant: "prompt.Build",
			Runs: 10, Errors: 0,
			Samples: []Sample{{Output: "commit"}},
			Asserts: []AssertionResult{
				{Label: "has-commit", Polarity: Must, Threshold: 0.8, Present: 9, Graded: 10, Pass: true},
			},
		},
	}
}

func TestText_TripWireAppearsWithOffendingOutput(t *testing.T) {
	var buf bytes.Buffer
	meta := Meta{Provider: "codestral", Model: "codestral-1", Variant: "prompt.Build", NPolicy: "adaptive(min=3,max=10)", Timestamp: time.Now()}
	if err := Text(&buf, sampleResults(), meta); err != nil {
		t.Fatalf("Text: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "TRIP WIRES TRIPPED") {
		t.Fatalf("want trip-wire section header, got:\n%s", out)
	}
	if !strings.Contains(out, `"&& bad"`) {
		t.Fatalf("want offending output quoted verbatim, got:\n%s", out)
	}
	if !strings.Contains(out, "A1") {
		t.Fatalf("want offending case ID in trip-wire section, got:\n%s", out)
	}
	if !strings.Contains(out, "do not read a 7/10 vs 8/10 difference as a difference") {
		t.Fatalf("want the exact caveat sentence in the footer, got:\n%s", out)
	}
}

func TestText_NoTripWiresReportsNone(t *testing.T) {
	var buf bytes.Buffer
	results := []CaseResult{{
		CaseID: "E1", Category: "context",
		Asserts: []AssertionResult{{Label: "has-commit", Polarity: Must, Present: 8, Graded: 10, Pass: true}},
	}}
	if err := Text(&buf, results, Meta{}); err != nil {
		t.Fatalf("Text: %v", err)
	}
	if !strings.Contains(buf.String(), "none tripped") {
		t.Fatalf("want a clean report to say no trip wires tripped, got:\n%s", buf.String())
	}
}

// TestText_NeverEvaluatedIncludesGraderErrorReason guards the diagnosability
// fix: a "NEVER EVALUATED" line by itself (just a count) is indistinguishable
// from a dozen other causes — diagnosing the live judge 404 required
// bypassing the harness entirely. The rendered report must name the reason.
func TestText_NeverEvaluatedIncludesGraderErrorReason(t *testing.T) {
	var buf bytes.Buffer
	results := []CaseResult{{
		CaseID: "C3", Category: "context",
		Asserts: []AssertionResult{{
			Label:            "plausible-next-command",
			Polarity:         Must,
			Graded:           0,
			GraderErrors:     3,
			FirstGraderError: "eval: judge request: 404 models/some-judge-model is not found for API version v1main",
		}},
	}}
	if err := Text(&buf, results, Meta{}); err != nil {
		t.Fatalf("Text: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "NEVER EVALUATED") {
		t.Fatalf("want a NEVER EVALUATED line, got:\n%s", out)
	}
	if !strings.Contains(out, "C3/plausible-next-command") {
		t.Fatalf("want the case/assertion identified, got:\n%s", out)
	}
	if !strings.Contains(out, "404 models/some-judge-model is not found") {
		t.Fatalf("want the grader error reason surfaced in the report, got:\n%s", out)
	}
}

func TestJSON_RoundTrips(t *testing.T) {
	meta := Meta{Provider: "codestral", Model: "codestral-1", Variant: "prompt.Build", NPolicy: "fixed=5", Timestamp: time.Now().Truncate(time.Second)}
	results := sampleResults()

	var buf bytes.Buffer
	if err := JSON(&buf, results, meta); err != nil {
		t.Fatalf("JSON: %v", err)
	}

	var got dump
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Meta.Provider != meta.Provider || got.Meta.Model != meta.Model || got.Meta.Variant != meta.Variant || got.Meta.NPolicy != meta.NPolicy {
		t.Fatalf("meta mismatch: got %+v, want %+v", got.Meta, meta)
	}
	if !got.Meta.Timestamp.Equal(meta.Timestamp) {
		t.Fatalf("timestamp mismatch: got %v, want %v", got.Meta.Timestamp, meta.Timestamp)
	}
	if len(got.Results) != len(results) {
		t.Fatalf("want %d results, got %d", len(results), len(got.Results))
	}
	if got.Results[0].CaseID != "A1" || got.Results[0].Asserts[0].FirstOffending != "&& bad" {
		t.Fatalf("round-tripped result mismatch: %+v", got.Results[0])
	}
}

func TestSample_JSONRoundTripsError(t *testing.T) {
	s := Sample{Err: errBoom}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got Sample
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Err == nil || got.Err.Error() != errBoom.Error() {
		t.Fatalf("want error message preserved, got %v", got.Err)
	}
}

func TestSample_JSONRoundTripsOutput(t *testing.T) {
	s := Sample{Output: "git status"}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got Sample
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Output != "git status" || got.Err != nil {
		t.Fatalf("want output preserved and no error, got %+v", got)
	}
}

// TestText_PivotsCellsIntoColumns is the scorecard's actual contract (plan
// doc: "case x (provider x prompt-variant) -> pass-rate"). Before this, Text
// rendered results as a flat list with no cell identity, so a two-provider
// run printed every case ID twice with nothing distinguishing the rows — the
// comparison the harness exists to produce was the one thing unreadable.
func TestText_PivotsCellsIntoColumns(t *testing.T) {
	mk := func(provider, variant string, present, graded int, pass bool) CaseResult {
		return CaseResult{
			CaseID: "A3", Category: "syntax",
			Provider: provider, Variant: variant, Runs: graded,
			Asserts: []AssertionResult{{
				Label: "supplies-leading-space", Polarity: Must, Threshold: 0.8,
				Present: present, Graded: graded, Pass: pass,
			}},
		}
	}
	results := []CaseResult{
		mk("groq", "default", 8, 10, true),
		mk("codestral", "default", 1, 10, false),
	}

	var buf bytes.Buffer
	if err := Text(&buf, results, Meta{}); err != nil {
		t.Fatalf("Text() err = %v", err)
	}
	out := buf.String()

	// One row for the case, not one per cell.
	if got := strings.Count(out, "supplies-leading-space"); got != 1 {
		t.Errorf("assertion appears on %d rows, want exactly 1 (pivoted, not repeated per cell):\n%s", got, out)
	}
	// Both cells appear as columns.
	for _, want := range []string{"groq/default", "codestral/default"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing cell column %q:\n%s", want, out)
		}
	}
	// And the two cells' verdicts are both present and distinguishable.
	if !strings.Contains(out, ". 80%") {
		t.Errorf("missing passing cell verdict %q:\n%s", ". 80%", out)
	}
	if !strings.Contains(out, "x 10%") {
		t.Errorf("missing failing cell verdict %q:\n%s", "x 10%", out)
	}
}

// TestText_FooterReportsP50LatencyPerCell pins the latency-comparison column:
// a cell's P50 is pooled across every case's successful samples (errored
// samples excluded, since they never got a TTFT), and a cell with no
// successful samples reports "n/a" rather than a misleading zero.
func TestText_FooterReportsP50LatencyPerCell(t *testing.T) {
	results := []CaseResult{
		{
			CaseID: "A1", Category: "syntax", Provider: "codestral", Variant: "default", Runs: 3,
			Samples: []Sample{
				{Output: "ok", TTFT: 100 * time.Millisecond},
				{Output: "ok", TTFT: 200 * time.Millisecond},
				{Output: "ok", TTFT: 300 * time.Millisecond},
			},
			Asserts: []AssertionResult{{Label: "a", Polarity: Measure, Present: 3, Graded: 3, Pass: true}},
		},
		{
			CaseID: "A2", Category: "syntax", Provider: "groq", Variant: "default", Runs: 0, Errors: 1,
			Samples: []Sample{{Err: errBoom}},
			Asserts: []AssertionResult{{Label: "a", Polarity: Measure, Graded: 0}},
		},
	}

	var buf bytes.Buffer
	if err := Text(&buf, results, Meta{}); err != nil {
		t.Fatalf("Text() err = %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "P50 LATENCY") {
		t.Fatalf("want a P50 LATENCY column header, got:\n%s", out)
	}
	if !strings.Contains(out, "200ms") {
		t.Errorf("want codestral/default's median (200ms) in the footer, got:\n%s", out)
	}
	if !strings.Contains(out, "groq") || !strings.Contains(out, "n/a") {
		t.Errorf("want groq/default reported as n/a (no successful samples), got:\n%s", out)
	}
}

// TestText_TripWireNamesTheCell pins that a tripped wire says WHICH cell
// tripped it: "A2 tripped" in a matrix run isn't actionable without it, and a
// wire that trips on one provider but not another is a different bug from one
// that trips on all of them.
func TestText_TripWireNamesTheCell(t *testing.T) {
	results := []CaseResult{{
		CaseID: "A2", Category: "syntax", Provider: "groq", Variant: "default", Runs: 3,
		Asserts: []AssertionResult{{
			Label: "double-space", Polarity: TripWire,
			Present: 1, Graded: 3, Pass: false, FirstOffending: "  status",
		}},
	}}

	var buf bytes.Buffer
	if err := Text(&buf, results, Meta{}); err != nil {
		t.Fatalf("Text() err = %v", err)
	}
	if !strings.Contains(buf.String(), "[groq/default] A2 double-space") {
		t.Errorf("trip-wire line does not name the cell:\n%s", buf.String())
	}
}
