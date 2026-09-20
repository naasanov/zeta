package eval

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/naasanov/zsh-autopilot/daemon/internal/provider"
)

// Limiter paces provider calls: Wait blocks until the next call is allowed;
// Observe reports what a completed call learned about the remote budget.
type Limiter interface {
	Wait(ctx context.Context) error
	Observe(rl *provider.RateLimit)
}

// NoopLimiter never waits.
type NoopLimiter struct{}

func (NoopLimiter) Wait(ctx context.Context) error { return ctx.Err() }
func (NoopLimiter) Observe(*provider.RateLimit)    {}

// RateLimiter paces calls to at most N per minute via a leaky bucket.
type RateLimiter struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
}

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

// Observe is a no-op: RateLimiter has no budget to learn from headers.
func (l *RateLimiter) Observe(*provider.RateLimit) {}

// AdaptiveLimiter paces calls against a token budget learned from response headers.
type AdaptiveLimiter struct {
	mu sync.Mutex

	started    bool
	limit      float64
	remaining  float64
	refillRate float64 // tokens/sec
	cost       float64 // conservative (running-max) tokens per call
	lastUpdate time.Time

	retryAfter time.Time // absolute deadline from the most recent 429
}

// costSafetyFactor inflates the learned cost before Wait reserves it.
const costSafetyFactor = 1.5

// NewAdaptiveLimiter returns a limiter that won't throttle until Observe
// learns rate-limit headers.
func NewAdaptiveLimiter() *AdaptiveLimiter {
	return &AdaptiveLimiter{}
}

// DefaultEvalConfigPath holds eval-only provider profiles, relative to the
// daemon module root.
const DefaultEvalConfigPath = "internal/eval/eval.toml"

// LimiterForBrand returns the rate limiter for calls to brand: groq gets the
// adaptive token-bucket AdaptiveLimiter; everything else gets NoopLimiter.
func LimiterForBrand(brand string) Limiter {
	switch brand {
	case "groq":
		return NewAdaptiveLimiter()
	default:
		return NoopLimiter{}
	}
}

// Wait blocks until the projected token budget covers one more call at the
// current cost estimate, or until an observed RetryAfter deadline passes,
// whichever is later, reserving the estimated cost before returning.
func (l *AdaptiveLimiter) Wait(ctx context.Context) error {
	l.mu.Lock()
	now := time.Now()

	var wait time.Duration
	if l.retryAfter.After(now) {
		wait = l.retryAfter.Sub(now)
	}

	if l.started && l.refillRate > 0 {
		if elapsed := now.Sub(l.lastUpdate).Seconds(); elapsed > 0 {
			l.remaining = min(l.remaining+elapsed*l.refillRate, l.limit)
		}
		reserve := l.cost * costSafetyFactor
		if deficit := reserve - l.remaining; deficit > 0 {
			if refillWait := time.Duration(deficit / l.refillRate * float64(time.Second)); refillWait > wait {
				wait = refillWait
			}
		}
		l.remaining -= reserve
		l.lastUpdate = now
	}
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

// Observe learns from one completed call: RetryAfter (a 429) raises the
// floor Wait must block until, and a valid LimitTokens/ResetTokens pair
// derives the refill rate and updates the running-max cost estimate.
func (l *AdaptiveLimiter) Observe(rl *provider.RateLimit) {
	if rl == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	if rl.RetryAfter > 0 {
		if until := now.Add(rl.RetryAfter); until.After(l.retryAfter) {
			l.retryAfter = until
		}
		// A 429 means the bucket is exhausted now, not whatever stale
		// positive value the last successful observation left behind.
		l.remaining = 0
		l.lastUpdate = now
	}

	if rl.LimitTokens <= 0 || rl.ResetTokens <= 0 || rl.RemainingTokens < 0 {
		return
	}
	limit := float64(rl.LimitTokens)
	remaining := float64(rl.RemainingTokens)
	if refillNeeded := limit - remaining; refillNeeded > 0 {
		if rate := refillNeeded / rl.ResetTokens.Seconds(); rate > 0 {
			l.refillRate = rate
		}
	}
	l.limit = limit

	if l.started && l.refillRate > 0 {
		if elapsed := now.Sub(l.lastUpdate).Seconds(); elapsed > 0 {
			projected := l.remaining + elapsed*l.refillRate
			if cost := projected - remaining; cost > l.cost {
				l.cost = cost
			}
		}
	}

	l.remaining = remaining
	l.lastUpdate = now
	l.started = true
}

// defaultMinRuns, defaultMaxRuns: adaptive sampling runs 3, escalating to 10
// on any disagreement.
const (
	defaultMinRuns = 3
	defaultMaxRuns = 10

	// maxRateLimitAttempts bounds how many times runOne retries a single
	// sample after a 429 before giving up and recording the error.
	maxRateLimitAttempts = 4

	// defaultConcurrency bounds how many Cases run at once; runs within one
	// case stay sequential.
	defaultConcurrency = 4
)

// Runner drives a set of Cases through a Provider with adaptive sampling.
type Runner struct {
	Provider provider.Provider
	Limiter  Limiter
	MinRuns  int // default 3
	MaxRuns  int // default 10
	FixedN   int // 0 = adaptive; otherwise exactly N, hard-capped at MaxRuns

	// Concurrency overrides defaultConcurrency (0 = default).
	Concurrency int

	// ProviderLabel overrides CaseResult.Provider with the name the user
	// selected (brand or profile), never the adapter, which several brands
	// share. Empty falls back to Provider.Name().
	ProviderLabel string

	// Progress, when non-nil, is called once per completed case, in case
	// order (not completion order) from one goroutine at a time without Run
	// holding a lock; a slow Progress serializes the pool.
	Progress func(Case, CaseResult)
}

// CaseSymbol is the one-character progress glyph for a finished case: '.'
// pass, 'x' at least one failed assertion, '!' a tripped trip-wire, 'E' no
// successful runs at all.
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

func (r *Runner) providerLabel() string {
	if r.ProviderLabel != "" {
		return r.ProviderLabel
	}
	return r.Provider.Name()
}

// resolved returns the effective min/max/concurrency/limiter, applying
// defaults for zero values without mutating the Runner.
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

// Run drives every case through r.Provider, returning one CaseResult per
// case in the same order as cases. A bounded worker pool writes each result
// only to its own index; no locking is needed for the writes.
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

	// The pool's out-of-order completions are re-emitted in index order:
	// `done` marks finished indices, `cursor` is the next unreported one.
	// Progress is called while holding progressMu; that lock is what orders output.
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
// into a CaseResult. Runs within a case are strictly sequential.
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
		var (
			completion provider.Completion
			err        error
		)
		// A throttled attempt is retried rather than recorded; latency
		// reflects only clean attempts. Retries are bounded, so a
		// persistent outage still surfaces as an error sample.
		for range maxRateLimitAttempts {
			if err = limiter.Wait(ctx); err != nil {
				break
			}
			completion, err = r.Provider.Complete(ctx, provider.Request{Req: c.Req})
			limiter.Observe(completion.RateLimit)
			if err == nil {
				break
			}
			var perr *provider.Error
			if !errors.As(err, &perr) || perr.Kind != provider.ErrRateLimited {
				break
			}
		}
		if err != nil {
			samples = append(samples, Sample{Err: err})
			return
		}
		// Trailing whitespace is trimmed to match what the user sees.
		// Leading whitespace stays untouched; it can be load-bearing.
		s := Sample{Output: strings.TrimRight(completion.Text, " \t\r\n"), TTFT: completion.TTFT}
		samples = append(samples, s)
		for i, a := range c.Asserts {
			ok, gerr := a.Grader.Grade(ctx, c.Req, s.Output)
			if gerr != nil {
				// Neither a run error nor a graded result, but still counted: an
				// assertion that silently never ran must not look like one that
				// ran and passed.
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
		// for any polarity: TripWire's "Present == 0" would otherwise read an
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
// AssertionResult.FirstGraderError retains.
const maxGraderErrorLen = 300

func truncateGraderError(msg string) string {
	r := []rune(msg)
	if len(r) <= maxGraderErrorLen {
		return msg
	}
	return string(r[:maxGraderErrorLen]) + "... (truncated)"
}

// allAgree reports whether every assertion's graded samples so far agree
// (all present or all absent). Zero graded samples agrees vacuously.
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
