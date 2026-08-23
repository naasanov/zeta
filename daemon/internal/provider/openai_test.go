package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/naasanov/zsh-autopilot/daemon/internal/prompt"
	"github.com/naasanov/zsh-autopilot/daemon/internal/protocol"
)

// testReq builds a provider.Request whose buffer renders into the chat-append
// user turn; the mock servers in this file don't inspect the request body,
// so only the buffer content is worth varying per test.
func testReq(buf string) Request {
	return Request{Req: protocol.Request{Buf: buf}, MaxTokens: 48}
}

func newOpenAI(t *testing.T, baseURL, model, apiKey string, maxTokens int) Provider {
	t.Helper()
	p, err := NewOpenAI(baseURL, model, apiKey, maxTokens, prompt.ShippedFor("openai").(prompt.ChatPrompt))
	if err != nil {
		t.Fatalf("NewOpenAI() err = %v, want nil", err)
	}
	return p
}

// sseChunk builds one SSE "data:" line carrying content as a chat-completions
// delta. json.Marshal escapes any embedded newline as `\n`, so the emitted
// line stays exactly one physical line, like a real provider's stream.
func sseChunk(t *testing.T, content string) string {
	t.Helper()
	payload := map[string]any{
		"choices": []map[string]any{
			{"delta": map[string]any{"content": content}},
		},
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal chunk: %v", err)
	}
	return "data: " + string(b) + "\n\n"
}

func TestComplete_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, part := range []string{"git ", "commit ", "-m \"wip\""} {
			fmt.Fprint(w, sseChunk(t, part))
			flusher.Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	client := newOpenAI(t, srv.URL, "test-model", "test-key", 48)
	got, err := client.Complete(context.Background(), testReq("git"))
	if err != nil {
		t.Fatalf("Complete() err = %v, want nil", err)
	}
	want := `git commit -m "wip"`
	if got.Text != want {
		t.Errorf("Complete().Text = %q, want %q", got.Text, want)
	}
	if got.HTTPStatus != http.StatusOK {
		t.Errorf("Complete().HTTPStatus = %d, want %d", got.HTTPStatus, http.StatusOK)
	}
}

// TestComplete_FirstLineCutoff: content spans a newline partway through, with
// a deliberately slow final chunk. Asserts only the text before the newline
// comes back, and Complete returns before the final chunk would arrive.
func TestComplete_FirstLineCutoff(t *testing.T) {
	const lateDelay = 300 * time.Millisecond

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		fmt.Fprint(w, sseChunk(t, "foo"))
		flusher.Flush()

		fmt.Fprint(w, sseChunk(t, " bar\n"))
		flusher.Flush()

		// A later chunk that, if consumed, would change the result. The
		// client must not wait for this: it already has a complete first
		// line after the previous chunk.
		time.Sleep(lateDelay)
		fmt.Fprint(w, sseChunk(t, "baz-should-not-appear"))
		flusher.Flush()
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	client := newOpenAI(t, srv.URL, "test-model", "test-key", 48)

	start := time.Now()
	got, err := client.Complete(context.Background(), testReq("user"))
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Complete() err = %v, want nil", err)
	}
	want := "foo bar"
	if got.Text != want {
		t.Errorf("Complete().Text = %q, want %q", got.Text, want)
	}
	if elapsed >= lateDelay {
		t.Errorf("Complete() took %v, want well under the %v late-chunk delay (client did not stop early)", elapsed, lateDelay)
	}
}

// TestComplete_Cancellation: a stream blocks indefinitely after its first
// chunk; cancelling ctx must abort it promptly with a context error.
func TestComplete_Cancellation(t *testing.T) {
	blockCh := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, sseChunk(t, "partial-no-newline"))
		flusher.Flush()
		<-blockCh // simulate a stalled stream that never completes on its own
	}))
	// Cleanup order is load-bearing: srv.Close() blocks until in-flight
	// handlers return, and the handler above is parked on <-blockCh, so the
	// channel MUST be closed before srv.Close() runs. Defers are LIFO, so
	// close(blockCh) is declared last to execute first.
	defer srv.Close()
	defer close(blockCh)

	client := newOpenAI(t, srv.URL, "test-model", "test-key", 48)
	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		_, err := client.Complete(ctx, testReq("user"))
		errCh <- err
	}()

	time.Sleep(50 * time.Millisecond) // let the request start and the first chunk arrive
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Complete() err = %v, want errors.Is(err, context.Canceled)", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Complete() did not return promptly after ctx cancellation (hung)")
	}
}

func TestComplete_HTTPError(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusInternalServerError} {
		t.Run(fmt.Sprintf("status_%d", status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				// The SDK decodes the body into *openai.Error, which requires
				// the standard OpenAI error object shape under "error" — a
				// flat {"error":"boom"} string fails that decode.
				fmt.Fprint(w, `{"error":{"message":"boom","type":"invalid_request_error","code":"boom_code","param":""}}`)
			}))
			defer srv.Close()

			client := newOpenAI(t, srv.URL, "test-model", "test-key", 48)
			got, err := client.Complete(context.Background(), testReq("user"))
			if err == nil {
				t.Fatalf("Complete() err = nil, want non-nil for status %d (got %q)", status, got.Text)
			}
			// METRICS(§12): HTTPStatus must still be populated on the error
			// return so the caller can log/emit the status of a failed call.
			if got.HTTPStatus != status {
				t.Errorf("Complete().HTTPStatus = %d, want %d", got.HTTPStatus, status)
			}
		})
	}
}

// METRICS(§12): TestComplete_UsageAndFinishReason: a stream ends (no newline,
// so the cutoff doesn't fire) with a trailing usage chunk and finish_reason,
// asserting both decode onto the returned Completion.
func TestComplete_UsageAndFinishReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"git status"},"finish_reason":null}]}`+"\n\n")
		flusher.Flush()
		fmt.Fprint(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":42,"completion_tokens":7,"prompt_tokens_details":{"cached_tokens":10}}}`+"\n\n")
		flusher.Flush()
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	client := newOpenAI(t, srv.URL, "test-model", "test-key", 48)
	got, err := client.Complete(context.Background(), testReq("user"))
	if err != nil {
		t.Fatalf("Complete() err = %v, want nil", err)
	}

	if got.Text != "git status" {
		t.Errorf("Complete().Text = %q, want %q", got.Text, "git status")
	}
	if got.StopReason != "stop" {
		t.Errorf("Complete().StopReason = %q, want %q", got.StopReason, "stop")
	}
	if got.InputTokens != 42 {
		t.Errorf("Complete().InputTokens = %d, want 42", got.InputTokens)
	}
	if got.OutputTokens != 7 {
		t.Errorf("Complete().OutputTokens = %d, want 7", got.OutputTokens)
	}
	if got.CachedTokens != 10 {
		t.Errorf("Complete().CachedTokens = %d, want 10", got.CachedTokens)
	}
	if got.TTFT <= 0 {
		t.Errorf("Complete().TTFT = %v, want > 0", got.TTFT)
	}
}

func TestOpenAI_RenderPrompt(t *testing.T) {
	client := newOpenAI(t, "http://unused", "test-model", "test-key", 48)
	req := testReq("user")

	got := client.RenderPrompt(req)
	want := RenderChatPrompt(prompt.ShippedFor("openai").(prompt.ChatPrompt).RenderChat(req.Req))
	if got != want {
		t.Errorf("RenderPrompt() = %q, want %q (RenderChatPrompt output)", got, want)
	}
}

func TestOpenAI_PromptName(t *testing.T) {
	client := newOpenAI(t, "http://unused", "test-model", "test-key", 48)
	if got, want := client.PromptName(), "chat-append"; got != want {
		t.Errorf("PromptName() = %q, want %q", got, want)
	}
}

func TestParseRateLimit(t *testing.T) {
	tests := []struct {
		name string
		hdr  http.Header
		want *RateLimit
	}{
		{
			name: "full headers, sub-second reset",
			hdr: http.Header{
				"X-Ratelimit-Limit-Tokens":     {"6000"},
				"X-Ratelimit-Remaining-Tokens": {"5990"},
				"X-Ratelimit-Reset-Tokens":     {"615ms"},
			},
			want: &RateLimit{LimitTokens: 6000, RemainingTokens: 5990, ResetTokens: 615 * time.Millisecond},
		},
		{
			name: "seconds reset",
			hdr: http.Header{
				"X-Ratelimit-Limit-Tokens":     {"6000"},
				"X-Ratelimit-Remaining-Tokens": {"120"},
				"X-Ratelimit-Reset-Tokens":     {"8.684s"},
			},
			want: &RateLimit{LimitTokens: 6000, RemainingTokens: 120, ResetTokens: 8684 * time.Millisecond},
		},
		{
			name: "hours-minutes-seconds reset",
			hdr: http.Header{
				"X-Ratelimit-Limit-Tokens":     {"6000"},
				"X-Ratelimit-Remaining-Tokens": {"0"},
				"X-Ratelimit-Reset-Tokens":     {"6h1m26.4s"},
			},
			want: &RateLimit{LimitTokens: 6000, RemainingTokens: 0, ResetTokens: 6*time.Hour + 1*time.Minute + 26400*time.Millisecond},
		},
		{
			name: "429 carries only retry-after, no ratelimit headers",
			hdr:  http.Header{"Retry-After": {"2"}},
			want: &RateLimit{RetryAfter: 2 * time.Second},
		},
		{
			name: "no rate-limit headers at all",
			hdr:  http.Header{"Content-Type": {"application/json"}},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseRateLimit(tt.hdr)
			if (got == nil) != (tt.want == nil) {
				t.Fatalf("parseRateLimit() = %+v, want %+v", got, tt.want)
			}
			if got == nil {
				return
			}
			if *got != *tt.want {
				t.Errorf("parseRateLimit() = %+v, want %+v", *got, *tt.want)
			}
		})
	}
}

// TestComplete_RateLimitHeaders_HappyPath exercises the common case: the
// early-return cutoff path (a newline in the first chunk) must still carry
// the rate-limit headers observed on the response.
func TestComplete_RateLimitHeaders_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-ratelimit-limit-tokens", "6000")
		w.Header().Set("x-ratelimit-remaining-tokens", "5990")
		w.Header().Set("x-ratelimit-reset-tokens", "615ms")
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, sseChunk(t, "git status\n"))
		flusher.Flush()
	}))
	defer srv.Close()

	client := newOpenAI(t, srv.URL, "test-model", "test-key", 48)
	got, err := client.Complete(context.Background(), testReq("user"))
	if err != nil {
		t.Fatalf("Complete() err = %v, want nil", err)
	}
	want := &RateLimit{LimitTokens: 6000, RemainingTokens: 5990, ResetTokens: 615 * time.Millisecond}
	if got.RateLimit == nil || *got.RateLimit != *want {
		t.Errorf("Complete().RateLimit = %+v, want %+v", got.RateLimit, want)
	}
}

// TestComplete_RateLimitHeaders_Absent asserts a response with no
// rate-limit headers yields a nil RateLimit, not a zero-valued one.
func TestComplete_RateLimitHeaders_Absent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, sseChunk(t, "git status\n"))
		flusher.Flush()
	}))
	defer srv.Close()

	client := newOpenAI(t, srv.URL, "test-model", "test-key", 48)
	got, err := client.Complete(context.Background(), testReq("user"))
	if err != nil {
		t.Fatalf("Complete() err = %v, want nil", err)
	}
	if got.RateLimit != nil {
		t.Errorf("Complete().RateLimit = %+v, want nil", got.RateLimit)
	}
}

// TestComplete_RateLimitHeaders_429 asserts a 429's retry-after reaches the
// caller through the error path, with no token fields fabricated.
func TestComplete_RateLimitHeaders_429(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// retry-after:0 keeps the test fast; TestParseRateLimit already
		// covers the "2" seconds-integer parse.
		w.Header().Set("retry-after", "0")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"message":"rate limited","type":"rate_limit_error","code":"429","param":""}}`)
	}))
	defer srv.Close()

	client := newOpenAI(t, srv.URL, "test-model", "test-key", 48)
	got, err := client.Complete(context.Background(), testReq("user"))
	if err == nil {
		t.Fatalf("Complete() err = nil, want non-nil for a 429")
	}
	want := &RateLimit{RetryAfter: 0}
	if got.RateLimit == nil || *got.RateLimit != *want {
		t.Errorf("Complete().RateLimit = %+v, want %+v", got.RateLimit, want)
	}
}

// TestRateLimitHolder_Merge drives rateLimitHolder.merge directly with
// synthetic observations (SDK retries are disabled, so Complete itself never
// produces more than one attempt) to assert the new merge semantics: token
// fields come from the most recent observation that carried them, and
// RetryAfter is kept as the max seen across every merge.
func TestRateLimitHolder_Merge(t *testing.T) {
	t.Run("later token fields replace earlier ones", func(t *testing.T) {
		h := &rateLimitHolder{}
		h.merge(&RateLimit{LimitTokens: 6000, RemainingTokens: 5990, ResetTokens: 615 * time.Millisecond}, true)
		h.merge(&RateLimit{LimitTokens: 6000, RemainingTokens: 5500, ResetTokens: 1200 * time.Millisecond}, true)

		want := &RateLimit{LimitTokens: 6000, RemainingTokens: 5500, ResetTokens: 1200 * time.Millisecond}
		if h.rl == nil || *h.rl != *want {
			t.Errorf("merge() = %+v, want %+v", h.rl, want)
		}
	})

	t.Run("a retry-after-only observation does not erase prior token fields", func(t *testing.T) {
		h := &rateLimitHolder{}
		h.merge(&RateLimit{LimitTokens: 6000, RemainingTokens: 5990, ResetTokens: 615 * time.Millisecond}, true)
		h.merge(&RateLimit{RetryAfter: 2 * time.Second}, false)

		want := &RateLimit{LimitTokens: 6000, RemainingTokens: 5990, ResetTokens: 615 * time.Millisecond, RetryAfter: 2 * time.Second}
		if h.rl == nil || *h.rl != *want {
			t.Errorf("merge() = %+v, want %+v", h.rl, want)
		}
	})

	t.Run("RetryAfter is kept as the max across attempts", func(t *testing.T) {
		h := &rateLimitHolder{}
		h.merge(&RateLimit{RetryAfter: 5 * time.Second}, false)
		h.merge(&RateLimit{RetryAfter: 2 * time.Second}, false)

		if h.rl == nil || h.rl.RetryAfter != 5*time.Second {
			t.Errorf("merge().RetryAfter = %v, want %v (the max)", h.rl, 5*time.Second)
		}
	})

	t.Run("a nil observation is a no-op", func(t *testing.T) {
		h := &rateLimitHolder{}
		h.merge(nil, false)
		if h.rl != nil {
			t.Errorf("merge(nil) = %+v, want nil", h.rl)
		}

		h.merge(&RateLimit{LimitTokens: 100}, true)
		h.merge(nil, false)
		want := &RateLimit{LimitTokens: 100}
		if h.rl == nil || *h.rl != *want {
			t.Errorf("merge(nil) after a real observation = %+v, want %+v (unchanged)", h.rl, want)
		}
	})
}

// TestComplete_NoRetryOnRateLimit asserts retries are disabled: a server
// that always 429s must see exactly one HTTP attempt, and the returned error
// still carries RateLimit.RetryAfter from that one attempt.
func TestComplete_NoRetryOnRateLimit(t *testing.T) {
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("retry-after", "3")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"message":"rate limited","type":"rate_limit_error","code":"429","param":""}}`)
	}))
	defer srv.Close()

	client := newOpenAI(t, srv.URL, "test-model", "test-key", 48)
	got, err := client.Complete(context.Background(), testReq("user"))
	if err == nil {
		t.Fatalf("Complete() err = nil, want non-nil for a 429")
	}
	if attempts != 1 {
		t.Errorf("server saw %d attempt(s), want exactly 1 (retries must be disabled)", attempts)
	}
	// METRICS(§12): HTTPStatus and RateLimit must both be populated on the
	// HTTP-error return path.
	if got.HTTPStatus != http.StatusTooManyRequests {
		t.Errorf("Complete().HTTPStatus = %d, want %d", got.HTTPStatus, http.StatusTooManyRequests)
	}
	want := &RateLimit{RetryAfter: 3 * time.Second}
	if got.RateLimit == nil || *got.RateLimit != *want {
		t.Errorf("Complete().RateLimit = %+v, want %+v", got.RateLimit, want)
	}
}
