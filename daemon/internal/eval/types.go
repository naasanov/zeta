// Package eval is the suggestion-quality evaluation harness.
package eval

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/naasanov/zsh-autopilot/daemon/internal/protocol"
)

// Polarity decides how a detected shape is scored. Graders only report
// presence; Polarity decides whether that presence is good, bad, or tracked.
type Polarity string

const (
	Must     Polarity = "must"     // presence is desired; Threshold is a floor
	MustNot  Polarity = "mustNot"  // presence is undesired; Threshold is a ceiling
	TripWire Polarity = "tripwire" // presence is a defect; ANY occurrence fails
	Measure  Polarity = "measure"  // presence is tracked, never asserted
)

// Grader reports whether the shape it looks for is present in out, the raw
// completion suffix (Sample.Output), never req.Buf+suffix. in carries the
// originating request for context-aware checks.
type Grader interface {
	Name() string
	Grade(ctx context.Context, in protocol.Request, out string) (present bool, err error)
}

// GraderFunc adapts a plain func to Grader, the same shape http.HandlerFunc
// takes for http.Handler.
type GraderFunc struct {
	N string
	F func(protocol.Request, string) (bool, error)
}

func (g GraderFunc) Name() string { return g.N }

func (g GraderFunc) Grade(_ context.Context, in protocol.Request, out string) (bool, error) {
	return g.F(in, out)
}

// CtxGraderFunc adapts a ctx-aware func to Grader, for graders that do I/O
// or must forward ctx to the graders they wrap.
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

// Sample is one provider call's outcome. Output holds the completion suffix
// (Completion.Text verbatim), never req.Buf+suffix; Err is set instead when
// the provider call itself failed, and an errored Sample is never graded.
type Sample struct {
	Output string
	Err    error

	// TTFT is zero when Err is set.
	TTFT time.Duration
}

// sampleJSON is Sample's wire shape: error is an interface with no exported
// fields, so it marshals to "{}" by default and loses the message. Err
// round-trips as a plain string instead.
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
	// not decide on, distinct from CaseResult.Errors (provider failures). A
	// grader that always errors yields Graded == 0, never a pass for any polarity.
	GraderErrors int

	// FirstGraderError is the first grader error's message, truncated. Only
	// the first is retained; later errors still count toward GraderErrors.
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
	// PromptName identifies the prompt the provider rendered with.
	PromptName string `json:"prompt"`
	Runs       int    `json:"runs"` // successful runs
	Errors     int    `json:"errors"`

	// Escalated records that adaptive sampling went past MinRuns because the
	// case's assertions disagreed.
	Escalated bool `json:"escalated"`

	// RenderedPrompt is the exact text the provider adapter would send for
	// this request, captured once per CaseResult.
	RenderedPrompt string `json:"rendered_prompt"`

	Samples []Sample          `json:"samples"`
	Asserts []AssertionResult `json:"asserts"`
}
