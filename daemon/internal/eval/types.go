// Package eval is the suggestion-quality evaluation harness (see
// .docs/eval_harness_plan.md). It drives provider.Provider with hand-written
// Cases, grades output with deterministic Graders or an LLM judge, and
// reports pass-RATES rather than pass/fail (sampling is adaptive, not fixed-N).
//
// Import invariant: nothing outside cmd/eval may import this package. Evals
// are slow, costly, rate-limited, and non-deterministic, so they must never
// run under `go test ./...`. cmd/eval is a `main` package specifically so
// `go build ./...` compile-checks this code while `go test ./...` never
// executes it.
package eval

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/naasanov/zsh-autopilot/daemon/internal/protocol"
)

// Polarity decides how a detected shape is scored. Graders NEVER encode
// polarity — a grader reports only whether a shape is PRESENT; the
// Assertion's Polarity decides whether presence is good, bad, or merely
// tracked. This split is what lets the same grader (e.g. "output contains a
// backtick") back both a `must` and a `mustNot` assertion in different
// cases.
type Polarity string

const (
	Must     Polarity = "must"     // presence is desired; Threshold is a floor
	MustNot  Polarity = "mustNot"  // presence is undesired; Threshold is a ceiling
	TripWire Polarity = "tripwire" // presence is a defect; ANY occurrence fails
	Measure  Polarity = "measure"  // presence is tracked, never asserted
)

// Grader reports whether the shape it looks for is present in out. in
// carries the originating request for context-aware checks (e.g. "does the
// suggestion name a file in in.DirEntries"). out is the raw completion
// suffix (Sample.Output), never req.Buf+suffix. ctx is threaded through for
// graders that do I/O (the LLM judge); GraderFunc drops it since
// deterministic graders never need it.
type Grader interface {
	Name() string
	Grade(ctx context.Context, in protocol.Request, out string) (present bool, err error)
}

// GraderFunc adapts a plain func to Grader, the same shape http.HandlerFunc
// takes for http.Handler. Its F takes no ctx: a deterministic grader has
// nothing to cancel, and making every one of them accept a parameter they
// discard would be noise at ~20 call sites.
type GraderFunc struct {
	N string
	F func(protocol.Request, string) (bool, error)
}

func (g GraderFunc) Name() string { return g.N }

func (g GraderFunc) Grade(_ context.Context, in protocol.Request, out string) (bool, error) {
	return g.F(in, out)
}

// CtxGraderFunc adapts a ctx-aware func to Grader. Two kinds of grader need
// it: those doing I/O (the LLM judge), and the combinators — AnyOf/AllOf/Not
// must FORWARD ctx to the graders they wrap. Building a combinator out of
// GraderFunc would silently drop it, so a judge nested inside AnyOf would
// quietly become uncancellable.
type CtxGraderFunc struct {
	N string
	F func(context.Context, protocol.Request, string) (bool, error)
}

func (g CtxGraderFunc) Name() string { return g.N }

func (g CtxGraderFunc) Grade(ctx context.Context, in protocol.Request, out string) (bool, error) {
	return g.F(ctx, in, out)
}

// Assertion pairs a Grader with how its result should be scored.
type Assertion struct {
	Label     string
	Polarity  Polarity
	Threshold float64 // fraction in [0,1]; ignored for TripWire and Measure
	Grader    Grader
}

// Case is one eval scenario: a protocol.Request to drive through
// provider.Complete, plus the assertions to grade the resulting completions
// against.
type Case struct {
	ID       string // "A1"
	Category string // "syntax", "fabrication", ...
	Req      protocol.Request
	Asserts  []Assertion
}

// Sample is one provider call's outcome for a Case.
//
// Output holds the completion SUFFIX (Completion.Text verbatim), never
// req.Buf+suffix — leading-space assertions (A2/A3) depend on Output
// starting exactly where the model's output starts.
//
// Err is set instead of Output when the provider call itself failed. An
// errored Sample is never graded (errors are not failures).
type Sample struct {
	Output string
	Err    error

	// TTFT is zero when Err is set. Pooled across a cell for the report's P50
	// LATENCY column.
	TTFT time.Duration
}

// sampleJSON is Sample's wire shape: error is an interface with no exported
// fields, so it marshals to "{}" by default and loses the message entirely.
// MarshalJSON/UnmarshalJSON round-trip Err as a plain string instead, which
// is enough for the Part 4 JSON diff (it compares messages, not error
// identity).
type sampleJSON struct {
	Output string `json:"output"`
	Err    string `json:"err,omitempty"`
	TTFTMs int64  `json:"ttft_ms,omitempty"`
}

func (s Sample) MarshalJSON() ([]byte, error) {
	aux := sampleJSON{Output: s.Output, TTFTMs: s.TTFT.Milliseconds()}
	if s.Err != nil {
		aux.Err = s.Err.Error()
	}
	return json.Marshal(aux)
}

func (s *Sample) UnmarshalJSON(b []byte) error {
	var aux sampleJSON
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	s.Output = aux.Output
	s.TTFT = time.Duration(aux.TTFTMs) * time.Millisecond
	s.Err = nil
	if aux.Err != "" {
		s.Err = errors.New(aux.Err)
	}
	return nil
}

// AssertionResult is one Assertion's outcome for a Case, aggregated across
// all of the case's successful Samples.
type AssertionResult struct {
	Label     string
	Polarity  Polarity
	Threshold float64
	Present   int // successful runs where the shape was present
	Graded    int // successful runs graded (excludes errored runs)

	// GraderErrors counts samples the provider returned but the Grader could
	// not decide on. Distinct from CaseResult.Errors (provider failures): a
	// grader that always errors yields Graded == 0, which must never read as
	// a pass for ANY polarity, including TripWire (Present == 0 alone would
	// look like a silent, never-evaluated green).
	GraderErrors int

	// FirstGraderError is the first grader error's message (truncated, see
	// truncateGraderError), kept for diagnosability. Only the first is
	// retained; later errors still count toward GraderErrors.
	FirstGraderError string

	Pass           bool
	FirstOffending string // TripWire only: the output that tripped it
}

// CaseResult is one Case's outcome: every Sample run for it, plus the scored
// AssertionResult for each of its Asserts.
type CaseResult struct {
	CaseID   string `json:"case_id"`
	Category string `json:"category"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// PromptName identifies the prompt the provider rendered with
	// (provider.Provider.PromptName), for report labeling.
	PromptName string `json:"prompt"`
	Runs       int    `json:"runs"` // successful runs
	Errors     int    `json:"errors"`

	// Escalated records that adaptive sampling went past MinRuns because the
	// case's assertions disagreed across samples. Reported rather than
	// inferred from Runs+Errors, which would silently mislead the moment
	// MinRuns is customised or a run errors out early.
	Escalated bool `json:"escalated"`

	// RenderedPrompt is the exact text the provider adapter would send for
	// this case's request (Provider.RenderPrompt), captured once per
	// CaseResult rather than per Sample — it's a pure function of Req,
	// identical across every sample in the case.
	RenderedPrompt string `json:"rendered_prompt"`

	Samples []Sample          `json:"samples"`
	Asserts []AssertionResult `json:"asserts"`
}
