package eval

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

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
