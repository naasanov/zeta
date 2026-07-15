package suggest

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/naasanov/zsh-autopilot/daemon/internal/metrics"
	"github.com/naasanov/zsh-autopilot/daemon/internal/protocol"
	"github.com/naasanov/zsh-autopilot/daemon/internal/provider"
)

// METRICS(§12): TestLLM_EmitsRequestEvent drives suggest.LLM against a fake
// OpenAI-compatible server and asserts the "request" event handed to emit
// carries the expected field mapping from the request + the provider's
// Completion stats. It also asserts existing (pre-metrics) behavior is
// unchanged: the reply's Suggestion is req.Buf + suffix.
func TestLLM_EmitsRequestEvent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":" status"},"finish_reason":null}]}`+"\n\n")
		flusher.Flush()
		fmt.Fprint(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":3}}}`+"\n\n")
		flusher.Flush()
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	client := provider.NewClient(srv.URL, "llama-3.3-70b-versatile", "test-key", 48)

	var got []metrics.RequestEvent
	emit := func(ev metrics.RequestEvent) { got = append(got, ev) }

	fn := LLM(client, slog.Default(), emit)

	req := protocol.Request{
		V:    protocol.Version,
		ID:   "sess-abc.7",
		Kind: protocol.KindTyping,
		Buf:  "git",
	}

	reply, err := fn(context.Background(), req)
	if err != nil {
		t.Fatalf("LLM() err = %v, want nil", err)
	}
	if want := "git status"; reply.Suggestion != want {
		t.Errorf("reply.Suggestion = %q, want %q", reply.Suggestion, want)
	}
	if reply.Source != protocol.SourceLLM {
		t.Errorf("reply.Source = %q, want %q", reply.Source, protocol.SourceLLM)
	}

	if len(got) != 1 {
		t.Fatalf("emit called %d times, want 1", len(got))
	}
	ev := got[0]

	if ev.Event != "request" {
		t.Errorf("ev.Event = %q, want %q", ev.Event, "request")
	}
	if ev.RequestID != req.ID {
		t.Errorf("ev.RequestID = %q, want %q", ev.RequestID, req.ID)
	}
	if want := "sess-abc"; ev.SessionID != want {
		t.Errorf("ev.SessionID = %q, want %q", ev.SessionID, want)
	}
	if ev.Trigger != protocol.KindTyping {
		t.Errorf("ev.Trigger = %q, want %q", ev.Trigger, protocol.KindTyping)
	}
	if ev.BufferLen != len(req.Buf) {
		t.Errorf("ev.BufferLen = %d, want %d", ev.BufferLen, len(req.Buf))
	}
	if ev.SuggestionLen != len(reply.Suggestion) {
		t.Errorf("ev.SuggestionLen = %d, want %d", ev.SuggestionLen, len(reply.Suggestion))
	}
	if ev.Source != protocol.SourceLLM {
		t.Errorf("ev.Source = %q, want %q", ev.Source, protocol.SourceLLM)
	}
	if ev.InputTokens != 10 {
		t.Errorf("ev.InputTokens = %d, want 10", ev.InputTokens)
	}
	if ev.OutputTokens != 2 {
		t.Errorf("ev.OutputTokens = %d, want 2", ev.OutputTokens)
	}
	if ev.CachedReadTokens != 3 {
		t.Errorf("ev.CachedReadTokens = %d, want 3", ev.CachedReadTokens)
	}
	if ev.StopReason != "stop" {
		t.Errorf("ev.StopReason = %q, want %q", ev.StopReason, "stop")
	}
	if ev.HTTPStatus != http.StatusOK {
		t.Errorf("ev.HTTPStatus = %d, want %d", ev.HTTPStatus, http.StatusOK)
	}
	if ev.PriceTableVersion != metrics.PriceTableVersion {
		t.Errorf("ev.PriceTableVersion = %d, want %d", ev.PriceTableVersion, metrics.PriceTableVersion)
	}
	if ev.Cancelled {
		t.Errorf("ev.Cancelled = true, want false")
	}
}

// METRICS(§12): TestLLM_NilEmitDoesNotPanic asserts the pre-metrics call
// shape (emit == nil) still works, matching main.go's echo-mode/metrics-off
// wiring.
func TestLLM_NilEmitDoesNotPanic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":" status\n"}}]}`+"\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	client := provider.NewClient(srv.URL, "test-model", "test-key", 48)
	fn := LLM(client, slog.Default(), nil)

	req := protocol.Request{V: protocol.Version, ID: "s.1", Kind: protocol.KindTyping, Buf: "git"}
	reply, err := fn(context.Background(), req)
	if err != nil {
		t.Fatalf("LLM() err = %v, want nil", err)
	}
	if want := "git status"; reply.Suggestion != want {
		t.Errorf("reply.Suggestion = %q, want %q", reply.Suggestion, want)
	}
}

// METRICS(§12): TestLLM_CancelledEmitsCancelledEvent asserts an in-flight
// cancellation (ctx.Err() != nil at the time Complete returns) produces an
// event with cancelled=true, cancelled_at_stage="in_flight", and that the
// original error is still returned unchanged.
func TestLLM_CancelledEmitsCancelledEvent(t *testing.T) {
	blockCh := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"partial"}}]}`+"\n\n")
		flusher.Flush()
		<-blockCh
	}))
	defer srv.Close()
	defer close(blockCh)

	client := provider.NewClient(srv.URL, "test-model", "test-key", 48)

	var got []metrics.RequestEvent
	emit := func(ev metrics.RequestEvent) { got = append(got, ev) }
	fn := LLM(client, slog.Default(), emit)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	req := protocol.Request{V: protocol.Version, ID: "s.1", Kind: protocol.KindTyping, Buf: "git"}
	_, err := fn(ctx, req)
	if err == nil {
		t.Fatalf("LLM() err = nil, want non-nil (ctx already cancelled)")
	}

	if len(got) != 1 {
		t.Fatalf("emit called %d times, want 1", len(got))
	}
	if !got[0].Cancelled {
		t.Errorf("ev.Cancelled = false, want true")
	}
	if got[0].CancelledAtStage != "in_flight" {
		t.Errorf("ev.CancelledAtStage = %q, want %q", got[0].CancelledAtStage, "in_flight")
	}
}
