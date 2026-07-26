package eval

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/naasanov/zsh-autopilot/daemon/internal/prompt"
	"github.com/naasanov/zsh-autopilot/daemon/internal/protocol"
	"github.com/naasanov/zsh-autopilot/daemon/internal/provider"
)

// Variant builds a provider-neutral prompt.Prompt from a request, under a
// name that identifies it in the scorecard. The "variant axis" (plan doc,
// "The variant axis") is how the harness answers prompt-shape questions: swap
// in a Build that mutates the Prompt (e.g. drop Prompt.History to compare
// fim-raw-history against fim-commented-history) without touching Runner or
// the cases.
//
// Name is a plain field rather than something derived from the function: a
// variant is usually a closure, and closure names ("eval.foo.func1") are both
// unstable and meaningless in a report that exists to be diffed across runs.
type Variant struct {
	Name  string
	Build func(protocol.Request) prompt.Prompt
}

// DefaultVariant is the unmodified pipeline: prompt.Build with no mutation.
// Deliberately named "default" rather than "fim-raw-history" — the same
// neutral Prompt renders as raw-history FIM through the codestral adapter and
// as chat-baseline through the others, so the name belongs to the
// provider×variant cell that Part 4 assembles, not to this func.
func DefaultVariant() Variant {
	return Variant{Name: "default", Build: prompt.Build}
}

// Limiter paces provider calls. Wait blocks until the caller is allowed to
// make its next call, or ctx is done. It's a seam so Part 4 can add
// per-provider token buckets without touching Runner.
type Limiter interface {
	Wait(ctx context.Context) error
}

// NoopLimiter never waits. It's the Runner default and what the stub
// provider / -dry-run path uses, since there is no real rate limit to
// respect against a fake in-process provider.
type NoopLimiter struct{}

func (NoopLimiter) Wait(ctx context.Context) error { return ctx.Err() }

// RateLimiter paces calls to at most N per minute via a simple leaky-bucket:
// each Wait call is allowed to return only `interval` after the previous one
// did. It does not itself cap concurrency — see Runner's doc comment on
// worker-pool concurrency, which is the knob for that.
type RateLimiter struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
}

// NewRateLimiter returns a RateLimiter allowing perMinute calls per minute.
// perMinute <= 0 is treated as 1 (the most conservative non-zero rate)
// rather than a divide-by-zero or an accidental unlimited rate.
func NewRateLimiter(perMinute int) *RateLimiter {
	if perMinute <= 0 {
		perMinute = 1
	}
	return &RateLimiter{interval: time.Minute / time.Duration(perMinute)}
}

func (l *RateLimiter) Wait(ctx context.Context) error {
	l.mu.Lock()
	now := time.Now()
	wait := time.Duration(0)
	if l.next.After(now) {
		wait = l.next.Sub(now)
	}
	base := now
	if l.next.After(base) {
		base = l.next
	}
	l.next = base.Add(l.interval)
	l.mu.Unlock()

	if wait <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// defaultMinRuns, defaultMaxRuns are the adaptive-sampling defaults from the
// plan doc ("Sampling: adaptive, not fixed-N"): run 3, escalate to 10 on any
// disagreement.
const (
	defaultMinRuns = 3
	defaultMaxRuns = 10

	// defaultConcurrency bounds how many Cases the Runner works on at once.
	// Runs *within* one case stay sequential (the plan doc: "Keep the
	// concurrency simple and obvious ... the runs within a case are few"),
	// so this is the only concurrency knob Part 1 needs.
	defaultConcurrency = 4
)

// Runner drives a set of Cases through a Provider with adaptive sampling.
type Runner struct {
	Provider provider.Provider
	Variant  Variant // func(protocol.Request) prompt.Prompt; default prompt.Build
	Limiter  Limiter
	MinRuns  int // default 3
	MaxRuns  int // default 10
	FixedN   int // 0 = adaptive; otherwise exactly N, hard-capped at MaxRuns
}

// resolved returns the effective min/max/variant/limiter, applying defaults
// for zero values without mutating the Runner (so a Runner is safe to reuse
// or share read-only across goroutines the caller might spawn).
func (r *Runner) resolved() (min, max int, variant Variant, limiter Limiter) {
	min, max = r.MinRuns, r.MaxRuns
	if min <= 0 {
		min = defaultMinRuns
	}
	if max <= 0 {
		max = defaultMaxRuns
	}
	variant = r.Variant
	if variant.Build == nil {
		variant = DefaultVariant()
	}
	limiter = r.Limiter
	if limiter == nil {
		limiter = NoopLimiter{}
	}
	return min, max, variant, limiter
}

// Run drives every case in cases through r.Provider and returns one
// CaseResult per case, in the same order as cases.
//
// # Goroutine lifecycle
//
// Run owns a small bounded worker pool, all of it contained within this one
// call:
//
//  1. Run allocates `results` sized len(cases) up front and starts
//     min(defaultConcurrency, len(cases)) worker goroutines via a
//     sync.WaitGroup, each ranging over a `jobs` channel of case indices.
//  2. Run itself (the caller's goroutine) sends every index 0..len(cases) on
//     `jobs`, then closes it. Close happens only after every send has
//     completed synchronously in this same goroutine, so no worker can ever
//     observe a closed-but-not-fully-drained channel racing a send.
//  3. Each worker computes results[idx] = r.runCase(...) and writes directly
//     to its own index. No mutex is needed for that write: every worker
//     owns a disjoint set of indices (one case is only ever processed by
//     the worker that received it), so there is no shared mutable state
//     between workers at all — the classic "shard by index, no lock needed"
//     pattern.
//  4. Run calls wg.Wait() after closing `jobs`. Workers exit their range
//     loop (and call wg.Done) once `jobs` is closed AND drained, which is
//     guaranteed to happen because nothing blocks a worker from consuming
//     jobs (runCase always returns — errors are captured as Sample.Err, not
//     propagated as a stuck call) and nothing blocks the send side from
//     finishing (the channel is buffered to len(cases), so every send
//     completes without needing a receiver first).
//
// Why it can't deadlock: the only channel (`jobs`) is closed exactly once,
// after all sends complete, by the same goroutine that owns the send side —
// so "send on closed channel" is impossible. The only blocking wait
// (wg.Wait) is on workers whose own blocking operations (Limiter.Wait,
// Provider.Complete) already take ctx and return promptly on
// cancellation, so a canceled ctx unblocks the whole pool rather than
// hanging it. Run does not read `results` until wg.Wait() returns, so the
// WaitGroup is the only synchronization needed for the writes to become
// visible to the caller (happens-before via sync.WaitGroup).
func (r *Runner) Run(ctx context.Context, cases []Case) []CaseResult {
	results := make([]CaseResult, len(cases))
	if len(cases) == 0 {
		return results
	}

	workers := min(defaultConcurrency, len(cases))

	jobs := make(chan int, len(cases))
	for i := range cases {
		jobs <- i
	}
	close(jobs)

	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for idx := range jobs {
				results[idx] = r.runCase(ctx, cases[idx])
			}
		}()
	}
	wg.Wait()

	return results
}

// runCase runs one case's samples (adaptively or fixed, per r's config),
// grades every successful sample against every assertion, and aggregates
// into a CaseResult. Runs within a case are strictly sequential — see
// Run's doc comment for why that's the deliberate simplicity choice.
func (r *Runner) runCase(ctx context.Context, c Case) CaseResult {
	minRuns, maxRuns, variant, limiter := r.resolved()

	var samples []Sample
	present := make([][]bool, len(c.Asserts)) // per-assertion, per-graded-sample
	graderErrs := make([]int, len(c.Asserts))
	firstOffending := make([]string, len(c.Asserts))
	offendingSet := make([]bool, len(c.Asserts))
	escalated := false

	runOne := func() {
		if err := limiter.Wait(ctx); err != nil {
			samples = append(samples, Sample{Err: err})
			return
		}
		p := variant.Build(c.Req)
		completion, err := r.Provider.Complete(ctx, provider.Request{Prompt: p})
		if err != nil {
			samples = append(samples, Sample{Err: err})
			return
		}
		// Mirror the one transformation the production path applies between
		// provider.Complete and the text the user actually sees:
		// internal/suggest.LLM trims trailing whitespace off the completion
		// before building reply.Suggestion. Grading untrimmed text would
		// measure a string no user is ever shown — and would do it precisely
		// where it matters most, since several category-A assertions turn on
		// exact leading/trailing whitespace. Leading whitespace is deliberately
		// NOT trimmed: it is load-bearing (the model must supply its own
		// separating space, see prompt.systemPrompt).
		s := Sample{Output: strings.TrimRight(completion.Text, " \t\r\n")}
		samples = append(samples, s)
		for i, a := range c.Asserts {
			ok, gerr := a.Grader.Grade(ctx, c.Req, s.Output)
			if gerr != nil {
				// A grader error excludes this (case, assertion, sample)
				// triple from grading — it is neither a run error (the
				// provider call succeeded) nor a graded result (the grader
				// itself couldn't decide). It IS counted, though: an
				// uncounted grader error is invisible, and an assertion that
				// silently never ran is worse than one that fails.
				graderErrs[i]++
				continue
			}
			present[i] = append(present[i], ok)
			if a.Polarity == TripWire && ok && !offendingSet[i] {
				firstOffending[i] = s.Output
				offendingSet[i] = true
			}
		}
	}

	if r.FixedN > 0 {
		n := r.FixedN
		if n > maxRuns {
			n = maxRuns
		}
		for i := 0; i < n; i++ {
			runOne()
		}
	} else {
		for i := 0; i < minRuns; i++ {
			runOne()
		}
		if !allAgree(present) {
			escalated = true
			for i := minRuns; i < maxRuns; i++ {
				runOne()
			}
		}
	}

	runs, errs := 0, 0
	for _, s := range samples {
		if s.Err != nil {
			errs++
		} else {
			runs++
		}
	}

	asserts := make([]AssertionResult, len(c.Asserts))
	for i, a := range c.Asserts {
		graded := len(present[i])
		hits := 0
		for _, ok := range present[i] {
			if ok {
				hits++
			}
		}
		ar := AssertionResult{
			Label:        a.Label,
			Polarity:     a.Polarity,
			Threshold:    a.Threshold,
			Present:      hits,
			Graded:       graded,
			GraderErrors: graderErrs[i],
		}
		// graded == 0 means this assertion was never actually evaluated (every
		// sample errored, or the grader itself did). That is never a pass, for
		// any polarity — including Measure, which reports nothing, and
		// TripWire, whose "Present == 0" would otherwise render an
		// unevaluated assertion as a confident green.
		switch {
		case graded == 0:
			ar.Pass = false
		case a.Polarity == Must:
			ar.Pass = float64(hits)/float64(graded) >= a.Threshold
		case a.Polarity == MustNot:
			ar.Pass = float64(hits)/float64(graded) < a.Threshold
		case a.Polarity == TripWire:
			ar.Pass = hits == 0
			if offendingSet[i] {
				ar.FirstOffending = firstOffending[i]
			}
		case a.Polarity == Measure:
			ar.Pass = true
		}
		asserts[i] = ar
	}

	return CaseResult{
		CaseID:    c.ID,
		Category:  c.Category,
		Provider:  r.Provider.Name(),
		Model:     r.Provider.Model(),
		Variant:   variant.Name,
		Runs:      runs,
		Errors:    errs,
		Escalated: escalated,
		Samples:   samples,
		Asserts:   asserts,
	}
}

// allAgree reports whether, for every assertion, all of its graded samples
// so far agree (all present, or all absent). An assertion with zero graded
// samples (every sample errored, or every grader call itself errored)
// agrees vacuously — it must not by itself force escalation, since more
// runs won't produce more grade-able data if the provider keeps erroring.
func allAgree(present [][]bool) bool {
	for _, ps := range present {
		if len(ps) == 0 {
			continue
		}
		first := ps[0]
		for _, p := range ps[1:] {
			if p != first {
				return false
			}
		}
	}
	return true
}
