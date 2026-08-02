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
