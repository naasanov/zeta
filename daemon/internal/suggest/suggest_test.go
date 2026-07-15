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

// newOpenAI is a small test helper around provider.NewOpenAI, which now
// returns an error (the Provider constructor signature added in T1).
func newOpenAI(t *testing.T, baseURL, model, apiKey string, maxTokens int) provider.Provider {
	t.Helper()
	p, err := provider.NewOpenAI(baseURL, model, apiKey, maxTokens)
	if err != nil {
		t.Fatalf("provider.NewOpenAI() err = %v, want nil", err)
	}
	return p
}

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

	client := newOpenAI(t, srv.URL, "llama-3.3-70b-versatile", "test-key", 48)

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
	if ev.Provider != "openai" {
		t.Errorf("ev.Provider = %q, want %q", ev.Provider, "openai")
	}
	if ev.Model != "llama-3.3-70b-versatile" {
		t.Errorf("ev.Model = %q, want %q", ev.Model, "llama-3.3-70b-versatile")
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

	client := newOpenAI(t, srv.URL, "test-model", "test-key", 48)
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

	client := newOpenAI(t, srv.URL, "test-model", "test-key", 48)

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

// stubProvider is a minimal provider.Provider for testing suggest.LLM
// without spinning up an httptest.Server — the payoff of taking the
// interface instead of a concrete *provider client (T1's whole point).
type stubProvider struct {
	completion provider.Completion
	err        error
	name       string
	model      string
}

func (s stubProvider) Complete(ctx context.Context, req provider.Request) (provider.Completion, error) {
	return s.completion, s.err
}
func (s stubProvider) Name() string  { return s.name }
func (s stubProvider) Model() string { return s.model }

// TestLLM_StubProvider demonstrates the new seam: suggest.LLM works against
// any provider.Provider, not just the httptest-backed openai client, and
// still preserves the req.Buf + suffix reply invariant and correctly
// forwards Name()/Model() into the emitted "request" event.
func TestLLM_StubProvider(t *testing.T) {
	stub := stubProvider{
		completion: provider.Completion{
			Text:         " status",
			InputTokens:  5,
			OutputTokens: 1,
			HTTPStatus:   200,
			StopReason:   "stop",
		},
		name:  "codestral",
		model: "codestral-latest",
	}

	var got []metrics.RequestEvent
	emit := func(ev metrics.RequestEvent) { got = append(got, ev) }
	fn := LLM(stub, slog.Default(), emit)

	req := protocol.Request{V: protocol.Version, ID: "s.1", Kind: protocol.KindTyping, Buf: "git"}
	reply, err := fn(context.Background(), req)
	if err != nil {
		t.Fatalf("LLM() err = %v, want nil", err)
	}
	if want := "git status"; reply.Suggestion != want {
		t.Errorf("reply.Suggestion = %q, want %q", reply.Suggestion, want)
	}

	if len(got) != 1 {
		t.Fatalf("emit called %d times, want 1", len(got))
	}
	if got[0].Provider != "codestral" {
		t.Errorf("ev.Provider = %q, want %q", got[0].Provider, "codestral")
	}
	if got[0].Model != "codestral-latest" {
		t.Errorf("ev.Model = %q, want %q", got[0].Model, "codestral-latest")
	}
}

// TestLLM_StubProviderErrorSetsErrorType asserts a *provider.Error on the
// failure path is unwrapped into the "request" event's ErrorType field.
func TestLLM_StubProviderErrorSetsErrorType(t *testing.T) {
	stub := stubProvider{
		err:   &provider.Error{Kind: provider.ErrRateLimited, HTTPStatus: 429, Provider: "codestral"},
		name:  "codestral",
		model: "codestral-latest",
	}

	var got []metrics.RequestEvent
	emit := func(ev metrics.RequestEvent) { got = append(got, ev) }
	fn := LLM(stub, slog.Default(), emit)

	req := protocol.Request{V: protocol.Version, ID: "s.1", Kind: protocol.KindTyping, Buf: "git"}
	_, err := fn(context.Background(), req)
	if err == nil {
		t.Fatalf("LLM() err = nil, want non-nil")
	}

	if len(got) != 1 {
		t.Fatalf("emit called %d times, want 1", len(got))
	}
	if got[0].ErrorType != string(provider.ErrRateLimited) {
		t.Errorf("ev.ErrorType = %q, want %q", got[0].ErrorType, provider.ErrRateLimited)
	}
}
