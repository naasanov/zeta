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

// Goes through the unexported newAnthropicClient constructor since
// NewAnthropic never sets a baseURL.
func newAnthropicTestClient(t *testing.T, baseURL string) Provider {
	t.Helper()
	p, err := newAnthropicClient("test-model", "test-key", 48, baseURL, prompt.ShippedFor("anthropic").(prompt.ChatPrompt))
	if err != nil {
		t.Fatalf("newAnthropicClient() err = %v, want nil", err)
	}
	return p
}

// The mock servers in this file don't inspect the request body, so only
// the buffer content is worth varying per test.
func testReqAnthropic(buf string) Request {
	return Request{Req: protocol.Request{Buf: buf}, MaxTokens: 48}
}

// The SDK's decoder reads the event type from the "event:" line, not the
// JSON payload's own "type" key, so every event below carries both.
func sseEvent(t *testing.T, eventType string, data any) string {
	t.Helper()
	b, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal %s event: %v", eventType, err)
	}
	return fmt.Sprintf("event: %s\ndata: %s\n\n", eventType, string(b))
}

func messageStartEvent() map[string]any {
	return map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            "msg_test",
			"type":          "message",
			"role":          "assistant",
			"content":       []any{},
			"model":         "test-model",
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage": map[string]any{
				"input_tokens":  0,
				"output_tokens": 0,
			},
		},
	}
}

func contentBlockStartEvent(index int) map[string]any {
	return map[string]any{
		"type":  "content_block_start",
		"index": index,
		"content_block": map[string]any{
			"type": "text",
			"text": "",
		},
	}
}

func textDeltaEvent(index int, text string) map[string]any {
	return map[string]any{
		"type":  "content_block_delta",
		"index": index,
		"delta": map[string]any{
			"type": "text_delta",
			"text": text,
		},
	}
}

func contentBlockStopEvent(index int) map[string]any {
	return map[string]any{
		"type":  "content_block_stop",
		"index": index,
	}
}

func messageDeltaEvent(stopReason string, usage map[string]any) map[string]any {
	delta := map[string]any{}
	if stopReason != "" {
		delta["stop_reason"] = stopReason
	}
	return map[string]any{
		"type":  "message_delta",
		"delta": delta,
		"usage": usage,
	}
}

const messageStopEventType = "message_stop"

func messageStopEvent() map[string]any {
	return map[string]any{"type": "message_stop"}
}

func TestAnthropicComplete_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, e := range []string{
			sseEvent(t, "message_start", messageStartEvent()),
			sseEvent(t, "content_block_start", contentBlockStartEvent(0)),
			sseEvent(t, "content_block_delta", textDeltaEvent(0, "git ")),
			sseEvent(t, "content_block_delta", textDeltaEvent(0, "commit ")),
			sseEvent(t, "content_block_delta", textDeltaEvent(0, "-m \"wip\"")),
			sseEvent(t, "content_block_stop", contentBlockStopEvent(0)),
			sseEvent(t, "message_delta", messageDeltaEvent("end_turn", map[string]any{"output_tokens": 5})),
			sseEvent(t, messageStopEventType, messageStopEvent()),
		} {
			fmt.Fprint(w, e)
			flusher.Flush()
		}
	}))
	defer srv.Close()

	client := newAnthropicTestClient(t, srv.URL)
	got, err := client.Complete(context.Background(), testReqAnthropic("git"))
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

func TestAnthropicComplete_FirstLineCutoff(t *testing.T) {
	const lateDelay = 300 * time.Millisecond

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		fmt.Fprint(w, sseEvent(t, "message_start", messageStartEvent()))
		flusher.Flush()
		fmt.Fprint(w, sseEvent(t, "content_block_start", contentBlockStartEvent(0)))
		flusher.Flush()
		fmt.Fprint(w, sseEvent(t, "content_block_delta", textDeltaEvent(0, "foo")))
		flusher.Flush()
		fmt.Fprint(w, sseEvent(t, "content_block_delta", textDeltaEvent(0, " bar\n")))
		flusher.Flush()

		// A later chunk that, if consumed, would change the result.
		time.Sleep(lateDelay)
		fmt.Fprint(w, sseEvent(t, "content_block_delta", textDeltaEvent(0, "baz-should-not-appear")))
		flusher.Flush()
		fmt.Fprint(w, sseEvent(t, "content_block_stop", contentBlockStopEvent(0)))
		flusher.Flush()
		fmt.Fprint(w, sseEvent(t, "message_delta", messageDeltaEvent("end_turn", map[string]any{"output_tokens": 9})))
		flusher.Flush()
		fmt.Fprint(w, sseEvent(t, messageStopEventType, messageStopEvent()))
		flusher.Flush()
	}))
	defer srv.Close()

	client := newAnthropicTestClient(t, srv.URL)

	start := time.Now()
	got, err := client.Complete(context.Background(), testReqAnthropic("user"))
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

func TestAnthropicComplete_Cancellation(t *testing.T) {
	blockCh := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, sseEvent(t, "message_start", messageStartEvent()))
		flusher.Flush()
		fmt.Fprint(w, sseEvent(t, "content_block_start", contentBlockStartEvent(0)))
		flusher.Flush()
		fmt.Fprint(w, sseEvent(t, "content_block_delta", textDeltaEvent(0, "partial-no-newline")))
		flusher.Flush()
		<-blockCh // simulate a stalled stream that never completes on its own
	}))
	// Cleanup order matters: srv.Close() blocks until in-flight handlers
	// return, and the handler is parked on <-blockCh, so the channel must
	// close first. Defers are LIFO, so close(blockCh) is declared last.
	defer srv.Close()
	defer close(blockCh)

	client := newAnthropicTestClient(t, srv.URL)
	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		_, err := client.Complete(ctx, testReqAnthropic("user"))
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

func TestAnthropicComplete_HTTPError(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusInternalServerError} {
		t.Run(fmt.Sprintf("status_%d", status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				fmt.Fprint(w, `{"type":"error","error":{"type":"invalid_request_error","message":"boom"}}`)
			}))
			defer srv.Close()

			client := newAnthropicTestClient(t, srv.URL)
			got, err := client.Complete(context.Background(), testReqAnthropic("user"))
			if err == nil {
				t.Fatalf("Complete() err = nil, want non-nil for status %d (got %q)", status, got.Text)
			}
			var perr *Error
			if !errors.As(err, &perr) {
				t.Fatalf("Complete() err = %v, want *provider.Error", err)
			}
			if perr.Provider != "anthropic" {
				t.Errorf("Error.Provider = %q, want %q", perr.Provider, "anthropic")
			}
			if got.HTTPStatus != status {
				t.Errorf("Complete().HTTPStatus = %d, want %d", got.HTTPStatus, status)
			}
			if perr.HTTPStatus != status {
				t.Errorf("Error.HTTPStatus = %d, want %d", perr.HTTPStatus, status)
			}
		})
	}
}

// No newline in the stream, so the cutoff never fires.
func TestAnthropicComplete_UsageAndFinishReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		fmt.Fprint(w, sseEvent(t, "message_start", messageStartEvent()))
		flusher.Flush()
		fmt.Fprint(w, sseEvent(t, "content_block_start", contentBlockStartEvent(0)))
		flusher.Flush()
		fmt.Fprint(w, sseEvent(t, "content_block_delta", textDeltaEvent(0, "git status")))
		flusher.Flush()
		fmt.Fprint(w, sseEvent(t, "content_block_stop", contentBlockStopEvent(0)))
		flusher.Flush()
		fmt.Fprint(w, sseEvent(t, "message_delta", messageDeltaEvent("end_turn", map[string]any{
			"input_tokens":            42,
			"output_tokens":           7,
			"cache_read_input_tokens": 10,
		})))
		flusher.Flush()
		fmt.Fprint(w, sseEvent(t, messageStopEventType, messageStopEvent()))
		flusher.Flush()
	}))
	defer srv.Close()

	client := newAnthropicTestClient(t, srv.URL)
	got, err := client.Complete(context.Background(), testReqAnthropic("user"))
	if err != nil {
		t.Fatalf("Complete() err = %v, want nil", err)
	}

	if got.Text != "git status" {
		t.Errorf("Complete().Text = %q, want %q", got.Text, "git status")
	}
	if got.StopReason != "end_turn" {
		t.Errorf("Complete().StopReason = %q, want %q", got.StopReason, "end_turn")
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

func TestAnthropic_NameAndModel(t *testing.T) {
	anthropicPrompt := prompt.ShippedFor("anthropic").(prompt.ChatPrompt)
	p, err := NewAnthropic("", "test-key", 48, anthropicPrompt)
	if err != nil {
		t.Fatalf("NewAnthropic() err = %v, want nil", err)
	}
	if p.Name() != "anthropic" {
		t.Errorf("Name() = %q, want %q", p.Name(), "anthropic")
	}
	if p.Model() != defaultAnthropicModel {
		t.Errorf("Model() = %q, want default %q", p.Model(), defaultAnthropicModel)
	}
	if p.PromptName() != anthropicPrompt.Name() {
		t.Errorf("PromptName() = %q, want %q", p.PromptName(), anthropicPrompt.Name())
	}

	p2, err := NewAnthropic("claude-opus-4-8", "test-key", 48, anthropicPrompt)
	if err != nil {
		t.Fatalf("NewAnthropic() err = %v, want nil", err)
	}
	if p2.Model() != "claude-opus-4-8" {
		t.Errorf("Model() = %q, want %q", p2.Model(), "claude-opus-4-8")
	}
}

func TestAnthropic_RenderPrompt(t *testing.T) {
	anthropicPrompt := prompt.ShippedFor("anthropic").(prompt.ChatPrompt)
	p, err := NewAnthropic("", "test-key", 48, anthropicPrompt)
	if err != nil {
		t.Fatalf("NewAnthropic() err = %v, want nil", err)
	}
	req := Request{Req: protocol.Request{Buf: "git sta"}}

	got := p.RenderPrompt(req)
	want := RenderChatPrompt(anthropicPrompt.RenderChat(req.Req))
	if got != want {
		t.Errorf("RenderPrompt() = %q, want %q (RenderChatPrompt output)", got, want)
	}
}
