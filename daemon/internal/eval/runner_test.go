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
	"github.com/naasanov/zsh-autopilot/daemon/internal/provider"
)

func TestLimiterForBrand(t *testing.T) {
	if _, ok := LimiterForBrand("groq").(*AdaptiveLimiter); !ok {
		t.Errorf("LimiterForBrand(%q) = %T, want *AdaptiveLimiter", "groq", LimiterForBrand("groq"))
	}
	for _, brand := range []string{"codestral", "anthropic", "ollama", "unknown-brand"} {
		if _, ok := LimiterForBrand(brand).(NoopLimiter); !ok {
			t.Errorf("LimiterForBrand(%q) = %T, want NoopLimiter", brand, LimiterForBrand(brand))
		}
	}
}

// ---- AdaptiveLimiter --------------------------------------------------

func TestAdaptiveLimiter_UnthrottledBeforeAnyObservation(t *testing.T) {
	l := NewAdaptiveLimiter()
	start := time.Now()
	for range 5 {
		if err := l.Wait(context.Background()); err != nil {
			t.Fatalf("Wait: %v", err)
		}
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("Wait blocked before any observation: elapsed=%v", elapsed)
	}
}

func TestAdaptiveLimiter_NilObservationIsSafe(t *testing.T) {
	l := NewAdaptiveLimiter()
	l.Observe(nil)
	if err := l.Wait(context.Background()); err != nil {
		t.Fatalf("Wait after nil Observe: %v", err)
	}
}

func TestAdaptiveLimiter_LearnsCostAndThrottles(t *testing.T) {
	l := NewAdaptiveLimiter()
	// Refill rate: 8000 tokens over 60s = ~133.3 tokens/sec.
	l.Observe(&provider.RateLimit{LimitTokens: 8000, RemainingTokens: 8000, ResetTokens: 60 * time.Second})
	// Second observation, 245 tokens consumed with negligible elapsed time
	// between the two calls: the running-max cost estimate should land near 245.
	l.Observe(&provider.RateLimit{LimitTokens: 8000, RemainingTokens: 7755, ResetTokens: time.Second})

	l.mu.Lock()
	cost := l.cost
	l.mu.Unlock()
	if cost < 200 || cost > 300 {
		t.Fatalf("learned cost estimate = %v, want roughly 245", cost)
	}

	// Drain the projected budget down near the cost estimate so the next
	// Wait must actually throttle.
	l.mu.Lock()
	l.remaining = cost - 1
	l.mu.Unlock()

	start := time.Now()
	if err := l.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if elapsed := time.Since(start); elapsed <= 0 {
		t.Fatalf("want Wait to block once projected remaining is below the cost estimate, elapsed=%v", elapsed)
	}
}

func TestAdaptiveLimiter_RefillBetweenObservationsDoesNotUnderestimateCost(t *testing.T) {
	l := NewAdaptiveLimiter()
	l.Observe(&provider.RateLimit{LimitTokens: 8000, RemainingTokens: 1000, ResetTokens: 60 * time.Second})

	l.mu.Lock()
	l.lastUpdate = time.Now().Add(-time.Second) // pretend a full second refilled since then
	l.mu.Unlock()

	// Naively, remaining barely dropped (1000 -> 900), but ~117 tokens/sec
	// refilled in that second, so the real cost was closer to 217, not 100.
	l.Observe(&provider.RateLimit{LimitTokens: 8000, RemainingTokens: 900, ResetTokens: 60 * time.Second})

	l.mu.Lock()
	cost := l.cost
	l.mu.Unlock()
	if cost <= 100 {
		t.Fatalf("cost estimate = %v, want it corrected upward past the naive 100-token delta", cost)
	}
}

func TestAdaptiveLimiter_RetryAfterForcesWait(t *testing.T) {
	l := NewAdaptiveLimiter()
	l.Observe(&provider.RateLimit{RetryAfter: 40 * time.Millisecond})

	start := time.Now()
	if err := l.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 30*time.Millisecond {
		t.Fatalf("want Wait to honor RetryAfter, elapsed=%v", elapsed)
	}
}

func TestAdaptiveLimiter_RetryAfterZeroesRemainingBudget(t *testing.T) {
	l := NewAdaptiveLimiter()
	l.Observe(&provider.RateLimit{LimitTokens: 8000, RemainingTokens: 5000, ResetTokens: 60 * time.Second})

	l.Observe(&provider.RateLimit{RetryAfter: 30 * time.Millisecond})

	l.mu.Lock()
	remaining := l.remaining
	l.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("remaining after a 429 = %v, want 0", remaining)
	}

	start := time.Now()
	if err := l.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 20*time.Millisecond {
		t.Fatalf("want Wait to block on the zeroed budget, elapsed=%v", elapsed)
	}
}

// TestAdaptiveLimiter_CostSafetyFactorThrottlesEarlierThanRawCost sets
// remaining exactly to the learned raw cost: the raw cost alone would leave
// a zero deficit, but costSafetyFactor inflates the reserve past it.
func TestAdaptiveLimiter_CostSafetyFactorThrottlesEarlierThanRawCost(t *testing.T) {
	l := NewAdaptiveLimiter()
	l.Observe(&provider.RateLimit{LimitTokens: 8000, RemainingTokens: 8000, ResetTokens: 60 * time.Second})
	l.Observe(&provider.RateLimit{LimitTokens: 8000, RemainingTokens: 7755, ResetTokens: time.Second})

	l.mu.Lock()
	l.remaining = l.cost
	l.mu.Unlock()

	start := time.Now()
	if err := l.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if elapsed := time.Since(start); elapsed <= 0 {
		t.Fatalf("want costSafetyFactor to block Wait even though remaining == raw cost, elapsed=%v", elapsed)
	}
}

func TestAdaptiveLimiter_WaitRespectsCtxCancellation(t *testing.T) {
	l := NewAdaptiveLimiter()
	l.Observe(&provider.RateLimit{RetryAfter: time.Hour})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	err := l.Wait(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait = %v, want context.DeadlineExceeded", err)
	}
}

// rlScriptEntry is one scripted Complete outcome for rlScriptProvider: unlike
// StubResult, it can attach a RateLimit to an ERRORED completion the way a
// real 429 response does.
type rlScriptEntry struct {
	completion provider.Completion
	err        error
}

// rlScriptProvider replays rlScriptEntry values in order (cycling once
// exhausted) and counts calls, for exercising runOne's rate-limit retry loop.
type rlScriptProvider struct {
	mu     sync.Mutex
	idx    int
	calls  int
	script []rlScriptEntry
}

func (p *rlScriptProvider) Complete(_ context.Context, _ provider.Request) (provider.Completion, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e := p.script[p.idx%len(p.script)]
	p.idx++
	p.calls++
	return e.completion, e.err
}

func (p *rlScriptProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func (p *rlScriptProvider) Name() string                         { return "stub" }
func (p *rlScriptProvider) Model() string                        { return "stub-1" }
func (p *rlScriptProvider) PromptName() string                   { return "stub" }
func (p *rlScriptProvider) RenderPrompt(provider.Request) string { return "" }

// rateLimitedErr is a *provider.Error classified the way a real 429 is, so
// errors.As(err, &perr) with perr.Kind == provider.ErrRateLimited matches it.
func rateLimitedErr() error {
	return &provider.Error{Kind: provider.ErrRateLimited, Provider: "groq", Err: errBoom}
}

// spyLimiter is a Limiter test double that counts Wait calls and records
// every Observe argument, so a test can assert the retry loop actually
// re-consults the limiter and feeds it what each attempt learned.
type spyLimiter struct {
	mu       sync.Mutex
	waits    int
	observed []*provider.RateLimit
}

func (l *spyLimiter) Wait(ctx context.Context) error {
	l.mu.Lock()
	l.waits++
	l.mu.Unlock()
	return ctx.Err()
}

func (l *spyLimiter) Observe(rl *provider.RateLimit) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.observed = append(l.observed, rl)
}

func (l *spyLimiter) snapshot() (waits int, observed []*provider.RateLimit) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.waits, append([]*provider.RateLimit(nil), l.observed...)
}

// TestRunCase_RateLimitedAttemptRetriedNotRecorded guards runOne's contract
// that a throttled attempt never becomes a Sample: only the eventual
// success does, carrying its own TTFT rather than the throttled attempt's.
func TestRunCase_RateLimitedAttemptRetriedNotRecorded(t *testing.T) {
	p := &rlScriptProvider{script: []rlScriptEntry{
		{err: rateLimitedErr()},
		{completion: provider.Completion{Text: "ok", TTFT: 42 * time.Millisecond}},
	}}
	r := &Runner{Provider: p, FixedN: 1}
	c := basicCase(Assertion{Label: "x", Polarity: Measure, Grader: containsGrader("o")})

	results := r.Run(context.Background(), []Case{c})
	res := results[0]
	if len(res.Samples) != 1 {
		t.Fatalf("want 1 sample, got %d", len(res.Samples))
	}
	if res.Samples[0].Err != nil {
		t.Fatalf("want the successful attempt's sample, got error %v", res.Samples[0].Err)
	}
	if res.Samples[0].TTFT != 42*time.Millisecond {
		t.Errorf("want TTFT = 42ms from the successful attempt, got %v", res.Samples[0].TTFT)
	}
	if got := p.callCount(); got != 2 {
		t.Errorf("want Complete called twice (throttled then success), got %d", got)
	}
}

// TestRunCase_RateLimitRetriesBounded guards maxRateLimitAttempts: a
// provider that is always rate-limited must not retry forever, and the
// eventual give-up records exactly one error sample.
func TestRunCase_RateLimitRetriesBounded(t *testing.T) {
	p := &rlScriptProvider{script: []rlScriptEntry{{err: rateLimitedErr()}}}
	r := &Runner{Provider: p, FixedN: 1}
	c := basicCase(Assertion{Label: "x", Polarity: Measure, Grader: containsGrader("o")})

	results := r.Run(context.Background(), []Case{c})
	res := results[0]
	if len(res.Samples) != 1 || res.Samples[0].Err == nil {
		t.Fatalf("want exactly 1 error sample, got %+v", res.Samples)
	}
	if got := p.callCount(); got != maxRateLimitAttempts {
		t.Errorf("want Complete called exactly %d times, got %d", maxRateLimitAttempts, got)
	}
}

// TestRunCase_NonRateLimitErrorNotRetried guards that the retry loop is
// specific to ErrRateLimited: any other error records immediately, with no
// wasted retry against a call that isn't going to succeed on its own.
func TestRunCase_NonRateLimitErrorNotRetried(t *testing.T) {
	p := &rlScriptProvider{script: []rlScriptEntry{{err: errBoom}}}
	r := &Runner{Provider: p, FixedN: 1}
	c := basicCase(Assertion{Label: "x", Polarity: Measure, Grader: containsGrader("o")})

	results := r.Run(context.Background(), []Case{c})
	res := results[0]
	if len(res.Samples) != 1 || res.Samples[0].Err == nil {
		t.Fatalf("want exactly 1 error sample, got %+v", res.Samples)
	}
	if got := p.callCount(); got != 1 {
		t.Errorf("want Complete called exactly once (no retry on a non-rate-limit error), got %d", got)
	}
}

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
	p := NewStubProvider("test",
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
	p := NewStubProvider("test", StubResult{Output: "d"})
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
	p := NewStubProvider("test", StubResult{Output: "d"}, StubResult{Output: "x"})
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
	p := NewStubProvider("test", StubResult{Output: "d"}, StubResult{Output: "x"})
	r := &Runner{Provider: p, FixedN: 5}
	c := basicCase(Assertion{Label: "has-d", Polarity: Measure, Grader: containsGrader("d")})

	results := r.Run(context.Background(), []Case{c})
	got := results[0]
	if got.Runs != 5 {
		t.Fatalf("want exactly 5 runs, got %d", got.Runs)
	}
}

func TestRun_FixedNClampedToMaxRuns(t *testing.T) {
	p := NewStubProvider("test", StubResult{Output: "d"})
	r := &Runner{Provider: p, FixedN: 1000, MaxRuns: 10}
	c := basicCase(Assertion{Label: "has-d", Polarity: Measure, Grader: containsGrader("d")})

	results := r.Run(context.Background(), []Case{c})
	got := results[0]
	if got.Runs != 10 {
		t.Fatalf("want FixedN clamped to MaxRuns=10, got %d", got.Runs)
	}
}

func TestRun_ErrorsExcludedFromGradingButCounted(t *testing.T) {
	p := NewStubProvider("test",
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
	p := NewStubProvider("test", StubResult{Err: errors.New("boom")})
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
	p := NewStubProvider("test",
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
	p := NewStubProvider("test", StubResult{Output: "ok"})
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
	p := NewStubProvider("test", StubResult{Output: "x"})
	r := &Runner{Provider: p, FixedN: 3}
	c := basicCase(Assertion{Label: "tracked", Polarity: Measure, Grader: containsGrader("d")})

	results := r.Run(context.Background(), []Case{c})
	if !results[0].Asserts[0].Pass {
		t.Fatalf("Measure assertions must always report Pass=true")
	}
}

func TestRun_MultipleCasesPreserveOrderAndIndependence(t *testing.T) {
	p := NewStubProvider("test", StubResult{Output: "d"})
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
	p := NewStubProvider("test", StubResult{Output: "d"})
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
			r := &Runner{Provider: NewStubProvider("test", StubResult{Output: " status"})}
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
	r := &Runner{Provider: NewStubProvider("test", StubResult{Output: " status"}), FixedN: 3}
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
		Provider: NewStubProvider("test", StubResult{Output: " x"}),
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
		Provider: NewStubProvider("test", StubResult{Output: " x"}),
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
