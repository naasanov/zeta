// Package eval is the suggestion-quality evaluation harness (see
// .docs/eval_harness_plan.md). It drives provider.Provider (the same seam
// internal/suggest programs against) with hand-written Cases, grades the
// output with deterministic Graders (and, in a later part, an LLM judge),
// and reports pass-rates rather than pass/fail — see the plan doc's
// "Sampling: adaptive, not fixed-N" section for why.
//
// This is Part 1: types, the adaptive-sampling Runner, and the report
// renderer, all driven by a scripted StubProvider. No real cases and no
// network calls live here yet (Parts 2/3).
//
// # Import invariant
//
// Nothing outside daemon/cmd/eval may import this package. Evals are slow,
// costly, rate-limited, and non-deterministic by construction — they must
// never run under `go test ./...`. cmd/eval is a `main` package specifically
// so `go build ./...` still compile-checks this code without a build tag,
// while `go test ./...` never executes it (Go doesn't run "main" packages as
// tests). Keep it that way: if you're tempted to import "internal/eval" from
// anywhere other than cmd/eval, that's a sign the thing you're writing
// belongs in cmd/eval instead.
package eval

import (
	"context"
	"encoding/json"
	"errors"

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
// carries the originating request so a grader can compare against context
// (dir entries, history, git branch) — e.g. "does the suggestion name a file
// that's actually in in.DirEntries".
//
// out is a Sample.Output: the raw completion suffix, NOT req.Buf+suffix. See
// Sample's doc comment for why.
// ctx is threaded through for graders that do I/O — in practice the LLM judge,
// which must be cancellable and time-limited like any other network call.
// Deterministic graders ignore it; GraderFunc drops it so the ~20 regex and
// string graders need no ctx parameter they would never use.
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
// prompt.Build (or a Variant) and provider.Complete, plus the assertions to
// grade the resulting completions against.
type Case struct {
	ID       string // "A1"
	Category string // "syntax", "fabrication", ...
	Req      protocol.Request
	Asserts  []Assertion
}

// Sample is one provider call's outcome for a Case.
//
// Output holds the completion SUFFIX, i.e. Completion.Text verbatim — NOT
// req.Buf+suffix. provider.Complete already returns only the suffix (see
// internal/suggest/suggest.go, which builds reply.Suggestion =
// req.Buf + completion.Text); there is no buffer prefix to strip here.
// Graders reason about the suffix, and leading-space assertions (A2/A3 in
// the plan doc) depend on that: " " vs "" only makes sense if Output starts
// exactly where the model's output starts.
//
// Err is set instead of Output when the provider call itself failed
// (network error, rate limit, canceled context). An errored Sample is never
// graded — see Runner's "errors are not failures" rule.
type Sample struct {
	Output string
	Err    error
}

// sampleJSON is Sample's wire shape: error is an interface with no exported
// fields, so it marshals to "{}" by default and loses the message entirely.
// MarshalJSON/UnmarshalJSON round-trip Err as a plain string instead, which
// is enough for the Part 4 JSON diff (it compares messages, not error
// identity).
type sampleJSON struct {
	Output string `json:"output"`
	Err    string `json:"err,omitempty"`
}

func (s Sample) MarshalJSON() ([]byte, error) {
	aux := sampleJSON{Output: s.Output}
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

	// GraderErrors counts samples the provider returned successfully but this
	// assertion's Grader could not decide on. It is NOT the same as
	// CaseResult.Errors (provider failures) and must stay separate: a grader
	// that always errors produces Graded == 0, and a "0 of 0" result must
	// never read as a pass for ANY polarity — including TripWire, whose
	// natural formulation (Present == 0) would otherwise report a silent
	// green for an assertion that was never actually evaluated.
	GraderErrors int

	Pass           bool
	FirstOffending string // TripWire only: the output that tripped it
}

// CaseResult is one Case's outcome: every Sample run for it, plus the scored
// AssertionResult for each of its Asserts.
type CaseResult struct {
	CaseID   string
	Category string
	Provider string
	Model    string
	Variant  string
	Runs     int // successful runs
	Errors   int

	// Escalated records that adaptive sampling went past MinRuns because the
	// case's assertions disagreed across samples. Reported rather than
	// inferred from Runs+Errors, which would silently mislead the moment
	// MinRuns is customised or a run errors out early.
	Escalated bool

	Samples []Sample
	Asserts []AssertionResult
}
