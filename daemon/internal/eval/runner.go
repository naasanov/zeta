package eval

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/naasanov/zsh-autopilot/daemon/internal/provider"
)

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

// groqPerMinute is kept a hair under groq's advertised 30/min free tier —
// field data showed 19% 429s even before the eval harness's own concurrent
// worker pool adds load.
const groqPerMinute = 25

// LimiterForBrand returns the rate limiter an eval run should use for calls
// to brand: groq is capped at groqPerMinute/min; codestral/anthropic and
// unknown brands get NoopLimiter (concurrency-bounded by the Runner's worker
// pool instead — an unrecognized brand will already have failed to resolve
// into a provider before a limiter matters).
func LimiterForBrand(brand string) Limiter {
	switch brand {
	case "groq":
		return NewRateLimiter(groqPerMinute)
	default:
		return NoopLimiter{}
	}
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

// defaultMinRuns, defaultMaxRuns: adaptive sampling runs 3, escalates to 10
// on any disagreement (see Runner.FixedN to opt out).
const (
	defaultMinRuns = 3
	defaultMaxRuns = 10

	// defaultConcurrency bounds how many Cases run at once; runs within one
	// case stay sequential. Only sensible without a shared Limiter — a cell
	// with a real rate limiter should set Runner.Concurrency instead.
	defaultConcurrency = 4
)

// Runner drives a set of Cases through a Provider with adaptive sampling.
type Runner struct {
	Provider provider.Provider
	Limiter  Limiter
	MinRuns  int // default 3
	MaxRuns  int // default 10
	FixedN   int // 0 = adaptive; otherwise exactly N, hard-capped at MaxRuns

	// Concurrency overrides defaultConcurrency (0 = default). With a shared
	// Limiter (e.g. groq's RateLimiter) extra workers only add queuing
	// latency, not throughput, and since Progress reports in case order,
	// spreading limited slots across concurrent cases delays the first
	// result for no gain — rate-limited cells should pass Concurrency: 1.
	Concurrency int

	// ProviderLabel overrides CaseResult.Provider and must be the brand
	// (codestral/anthropic/groq/ollama), not Provider.Name() (the adapter):
	// several brands share the openai adapter, so Name() alone would report
	// groq as "openai" and collide with it in the scorecard. Empty falls
	// back to Provider.Name().
	ProviderLabel string

	// Progress, when non-nil, is called once per completed case, in CASE
	// ORDER (not completion order, even though cases run concurrently) and
	// from one goroutine at a time without Run holding a lock — safe to
	// write to a shared io.Writer, but a slow Progress serializes the pool.
	Progress func(Case, CaseResult)
}

// CaseSymbol is the one-character progress glyph for a finished case, in the
// spirit of pytest's dots: pass is quiet, anything else is loud.
//
//	'.' every assertion passed
//	'x' at least one assertion failed
//	'!' a trip-wire tripped — a defect in shipped logic, not a quality miss,
//	    so it reads differently from an ordinary threshold miss at a glance
//	'E' the case produced no successful runs at all (provider errors), so
//	    nothing was actually evaluated — distinct from "evaluated and failed"
func CaseSymbol(r CaseResult) rune {
	if r.Runs == 0 {
		return 'E'
	}
	symbol := '.'
	for _, a := range r.Asserts {
		if a.Pass {
			continue
		}
		if a.Polarity == TripWire {
			return '!'
		}
		symbol = 'x'
	}
	return symbol
}

// providerLabel is the brand name to record on results: ProviderLabel when
// set, else the adapter's own Name(). See the ProviderLabel field.
func (r *Runner) providerLabel() string {
	if r.ProviderLabel != "" {
		return r.ProviderLabel
	}
	return r.Provider.Name()
}

// resolved returns the effective min/max/concurrency/limiter, applying
// defaults for zero values without mutating the Runner (so a Runner is safe
// to reuse or share read-only across goroutines the caller might spawn).
func (r *Runner) resolved() (min, max, concurrency int, limiter Limiter) {
	min, max = r.MinRuns, r.MaxRuns
	if min <= 0 {
		min = defaultMinRuns
	}
	if max <= 0 {
		max = defaultMaxRuns
	}
	concurrency = r.Concurrency
	if concurrency <= 0 {
		concurrency = defaultConcurrency
	}
	limiter = r.Limiter
	if limiter == nil {
		limiter = NoopLimiter{}
	}
	return min, max, concurrency, limiter
}

// Run drives every case through r.Provider and returns one CaseResult per
// case, in the same order as cases. A bounded worker pool consumes case
// indices from a channel and each worker writes only to its own result
// index, so no locking is needed for the writes; ctx cancellation unblocks
// workers via Limiter.Wait/Provider.Complete rather than hanging the pool.
func (r *Runner) Run(ctx context.Context, cases []Case) []CaseResult {
	results := make([]CaseResult, len(cases))
	if len(cases) == 0 {
		return results
	}

	_, _, concurrency, _ := r.resolved()
	workers := min(concurrency, len(cases))

	jobs := make(chan int, len(cases))
	for i := range cases {
		jobs <- i
	}
	close(jobs)

	// Progress reporting turns the pool's out-of-order completions back into
	// in-order emissions: `done` marks which indices have finished, `cursor`
	// is the next index not yet reported. A worker that finishes index 7
	// while 5 is still running reports nothing; whoever finishes 5 then
	// drains 5, 6, 7 in one go.
	//
	// The Progress callback is invoked WHILE HOLDING progressMu, and that is
	// load-bearing rather than lazy. Handing out disjoint [start,end) spans
	// under the lock and emitting outside it looks safe and is not: a worker
	// holding span [8,12) can finish its writes before the worker holding
	// [5,8) does, so the symbols still land out of order. Serializing the
	// emission itself is what actually orders the output. progressMu is
	// unexported and local to this call, so a callback cannot reach it — the
	// only way to deadlock here is a callback that re-enters this same Run,
	// which is why the field's doc says to keep it to formatting and a write.
	var (
		progressMu sync.Mutex
		done       []bool
		cursor     int
	)
	if r.Progress != nil {
		done = make([]bool, len(cases))
	}

	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for idx := range jobs {
				results[idx] = r.runCase(ctx, cases[idx])

				if r.Progress == nil {
					continue
				}
				progressMu.Lock()
				done[idx] = true
				for cursor < len(done) && done[cursor] {
					r.Progress(cases[cursor], results[cursor])
					cursor++
				}
				progressMu.Unlock()
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
	minRuns, maxRuns, _, limiter := r.resolved()

	var samples []Sample
	present := make([][]bool, len(c.Asserts)) // per-assertion, per-graded-sample
	graderErrs := make([]int, len(c.Asserts))
	firstGraderErr := make([]string, len(c.Asserts))
	graderErrSet := make([]bool, len(c.Asserts))
	firstOffending := make([]string, len(c.Asserts))
	offendingSet := make([]bool, len(c.Asserts))
	escalated := false

	runOne := func() {
		if err := limiter.Wait(ctx); err != nil {
			samples = append(samples, Sample{Err: err})
			return
		}
		completion, err := r.Provider.Complete(ctx, provider.Request{Req: c.Req})
		if err != nil {
			samples = append(samples, Sample{Err: err})
			return
		}
		// Mirror internal/suggest.LLM's trailing-whitespace trim so grading
		// sees what the user sees. Leading whitespace stays untouched — it's
		// load-bearing (see prompt.systemPrompt).
		s := Sample{Output: strings.TrimRight(completion.Text, " \t\r\n"), TTFT: completion.TTFT}
		samples = append(samples, s)
		for i, a := range c.Asserts {
			ok, gerr := a.Grader.Grade(ctx, c.Req, s.Output)
			if gerr != nil {
				// Neither a run error nor a graded result, but still counted:
				// an assertion that silently never ran must not look the
				// same as "ran and passed" (see CLAUDE.md: Graded == 0 never
				// passes).
				graderErrs[i]++
				if !graderErrSet[i] {
					firstGraderErr[i] = truncateGraderError(gerr.Error())
					graderErrSet[i] = true
				}
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
		n := min(r.FixedN, maxRuns)
		for range n {
			runOne()
		}
	} else {
		for range minRuns {
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
			Label:            a.Label,
			Polarity:         a.Polarity,
			Threshold:        a.Threshold,
			Present:          hits,
			Graded:           graded,
			GraderErrors:     graderErrs[i],
			FirstGraderError: firstGraderErr[i],
		}
		// graded == 0 (every sample or grader call errored) is never a pass,
		// for any polarity — TripWire's "Present == 0" would otherwise read
		// an unevaluated assertion as a confident green.
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
		CaseID:         c.ID,
		Category:       c.Category,
		Provider:       r.providerLabel(),
		Model:          r.Provider.Model(),
		PromptName:     r.Provider.PromptName(),
		Runs:           runs,
		Errors:         errs,
		Escalated:      escalated,
		RenderedPrompt: r.Provider.RenderPrompt(provider.Request{Req: c.Req}),
		Samples:        samples,
		Asserts:        asserts,
	}
}

// maxGraderErrorLen bounds how much of a grader error message
// AssertionResult.FirstGraderError retains. API error bodies can be
// arbitrarily large; a prefix truncation is enough since the identifying
// bits (HTTP status, model id) sit in the first line or two.
const maxGraderErrorLen = 300

// truncateGraderError truncates msg to maxGraderErrorLen runes, appending a
// marker so a truncated message is visibly not the whole story rather than
// looking like a short, complete one.
func truncateGraderError(msg string) string {
	r := []rune(msg)
	if len(r) <= maxGraderErrorLen {
		return msg
	}
	return string(r[:maxGraderErrorLen]) + "... (truncated)"
}

// allAgree reports whether every assertion's graded samples so far agree
// (all present or all absent). Zero graded samples agrees vacuously, since
// more runs won't help if the provider keeps erroring.
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
