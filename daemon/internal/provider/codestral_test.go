package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/naasanov/zsh-autopilot/daemon/internal/prompt"
)

func newCodestral(t *testing.T, baseURL, model, apiKey string, maxTokens int) Provider {
	t.Helper()
	p, err := NewCodestral(baseURL, model, apiKey, maxTokens)
	if err != nil {
		t.Fatalf("NewCodestral() err = %v, want nil", err)
	}
	return p
}

// fimSSEChunk builds one SSE "data:" line carrying content as a FIM
// completions delta, matching fimChunk's shape. Codestral's streaming
// response is OpenAI-shaped, so this mirrors openai_test.go's sseChunk.
func fimSSEChunk(t *testing.T, content string) string {
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

func TestComplete_Codestral_HappyPath(t *testing.T) {
	var gotBody fimRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/fim/completions" {
			t.Errorf("request path = %q, want %q", r.URL.Path, "/v1/fim/completions")
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, part := range []string{"git ", "commit ", "-m \"wip\""} {
			fmt.Fprint(w, fimSSEChunk(t, part))
			flusher.Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	client := newCodestral(t, srv.URL, "test-model", "test-key", 48)
	req := Request{Prompt: prompt.Prompt{Prefix: "git"}, MaxTokens: 48}
	got, err := client.Complete(context.Background(), req)
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
	// Request shape must be prompt/suffix, not messages — the whole reason
	// this is its own adapter rather than another OpenAI-compatible base URL.
	if gotBody.Prompt != "git" {
		t.Errorf("request Prompt = %q, want %q", gotBody.Prompt, "git")
	}
	if gotBody.Suffix != "" {
		t.Errorf("request Suffix = %q, want empty", gotBody.Suffix)
	}
	// Stop sequences must reach the wire — they are what keep a code model
	// from completing a partial command into a whole chained one-liner
	// (";"/"&&") that the newline cutoff can't catch.
	if !slices.Equal(gotBody.Stop, fimStopSequences) {
		t.Errorf("request Stop = %q, want %q", gotBody.Stop, fimStopSequences)
	}
}

// TestComplete_FirstLineCutoff drives a stream whose content spans a newline
// partway through, with a deliberately slow final chunk. It asserts both that
// (a) only the text before the newline comes back, and (b) Complete returns
// long before the final chunk would have been sent, proving the client
// stopped reading early rather than happening to produce the right prefix
// after consuming everything.
func TestComplete_Codestral_FirstLineCutoff(t *testing.T) {
	const lateDelay = 300 * time.Millisecond

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		fmt.Fprint(w, fimSSEChunk(t, "foo"))
		flusher.Flush()

		fmt.Fprint(w, fimSSEChunk(t, " bar\n"))
		flusher.Flush()

		// A later chunk that, if consumed, would change the result. The
		// client must not wait for this: it already has a complete first
		// line after the previous chunk.
		time.Sleep(lateDelay)
		fmt.Fprint(w, fimSSEChunk(t, "baz-should-not-appear"))
		flusher.Flush()
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	client := newCodestral(t, srv.URL, "test-model", "test-key", 48)

	start := time.Now()
	got, err := client.Complete(context.Background(), Request{Prompt: prompt.Prompt{Prefix: "user"}, MaxTokens: 48})
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

// TestComplete_Cancellation forces a stream that blocks indefinitely after
// its first chunk, then cancels the ctx passed to Complete. It asserts
// Complete returns promptly (not after the block would otherwise clear) with
// a context error, proving the in-flight HTTP call is actually aborted by ctx
// cancellation and the call does not hang.
func TestComplete_Codestral_Cancellation(t *testing.T) {
	blockCh := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, fimSSEChunk(t, "partial-no-newline"))
		flusher.Flush()
		<-blockCh // simulate a stalled stream that never completes on its own
	}))
	// Cleanup order is load-bearing: srv.Close() blocks until in-flight
	// handlers return, and the handler above is parked on <-blockCh, so the
	// channel MUST be closed before srv.Close() runs. Defers are LIFO, so
	// close(blockCh) is declared last to execute first.
	defer srv.Close()
	defer close(blockCh)

	client := newCodestral(t, srv.URL, "test-model", "test-key", 48)
	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		_, err := client.Complete(ctx, Request{Prompt: prompt.Prompt{Prefix: "user"}, MaxTokens: 48})
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

func TestComplete_Codestral_HTTPError(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusInternalServerError} {
		t.Run(fmt.Sprintf("status_%d", status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				fmt.Fprint(w, `{"error":"boom"}`)
			}))
			defer srv.Close()

			client := newCodestral(t, srv.URL, "test-model", "test-key", 48)
			got, err := client.Complete(context.Background(), Request{Prompt: prompt.Prompt{Prefix: "user"}, MaxTokens: 48})
			if err == nil {
				t.Fatalf("Complete() err = nil, want non-nil for status %d (got %q)", status, got.Text)
			}
			// METRICS(§12): HTTPStatus must still be populated on the error
			// return so the caller can log/emit the status of a failed call.
			if got.HTTPStatus != status {
				t.Errorf("Complete().HTTPStatus = %d, want %d", got.HTTPStatus, status)
			}
			var perr *Error
			if !errors.As(err, &perr) {
				t.Fatalf("Complete() err = %v, want *provider.Error", err)
			}
			if perr.Provider != "codestral" {
				t.Errorf("Error.Provider = %q, want %q", perr.Provider, "codestral")
			}
		})
	}
}

// METRICS(§12): TestComplete_UsageAndFinishReason drives a stream that ends
// (no newline in the content, so the first-line cutoff doesn't fire) with a
// trailing usage chunk and a finish_reason, asserting both decode onto the
// returned Completion.
func TestComplete_Codestral_UsageAndFinishReason(t *testing.T) {
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

	client := newCodestral(t, srv.URL, "test-model", "test-key", 48)
	got, err := client.Complete(context.Background(), Request{Prompt: prompt.Prompt{Prefix: "user"}, MaxTokens: 48})
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

// TestNewCodestral_Defaults asserts the documented empty-value defaults:
// baseURL "https://api.mistral.ai", model "codestral-latest". Constructed
// indirectly via Model()/the request URL prefix rather than reaching into
// unexported fields, since the constructor returns the Provider interface.
func TestNewCodestral_Defaults(t *testing.T) {
	p, err := NewCodestral("", "", "test-key", 48)
	if err != nil {
		t.Fatalf("NewCodestral() err = %v, want nil", err)
	}
	if p.Name() != "codestral" {
		t.Errorf("Name() = %q, want %q", p.Name(), "codestral")
	}
	if p.Model() != "codestral-latest" {
		t.Errorf("Model() = %q, want %q", p.Model(), "codestral-latest")
	}
	c := p.(*codestralClient)
	if c.baseURL != "https://api.mistral.ai" {
		t.Errorf("baseURL = %q, want %q", c.baseURL, "https://api.mistral.ai")
	}
}

// TestRenderFIM_ContextPresent pins the exact target shape from the T2c
// contract: context lines re-rendered as "#"-prefixed shell comments, in
// order, with the buffer last and no trailing newline.
func TestRenderFIM_ContextPresent(t *testing.T) {
	p := prompt.Prompt{
		System:      "system prompt text",
		Instruction: "instruction text",
		Context:     "Context:\n- cwd: /Users/x/proj\n- git: branch main (dirty)\n\n",
		Prefix:      "git com",
		Suffix:      "",
	}
	gotPrompt, gotSuffix := RenderFIM(p)
	want := "# cwd: /Users/x/proj\n# git: branch main (dirty)\ngit com"
	if gotPrompt != want {
		t.Errorf("RenderFIM() prompt = %q, want %q", gotPrompt, want)
	}
	if gotSuffix != "" {
		t.Errorf("RenderFIM() suffix = %q, want empty", gotSuffix)
	}
}

// TestRenderFIM_ContextAbsent checks the empty-context case is just the
// buffer, with no stray comment lines or leading newline.
func TestRenderFIM_ContextAbsent(t *testing.T) {
	p := prompt.Prompt{
		System:      "system prompt text",
		Instruction: "instruction text",
		Context:     "",
		Prefix:      "git com",
		Suffix:      "",
	}
	gotPrompt, gotSuffix := RenderFIM(p)
	if gotPrompt != "git com" {
		t.Errorf("RenderFIM() prompt = %q, want %q", gotPrompt, "git com")
	}
	if gotSuffix != "" {
		t.Errorf("RenderFIM() suffix = %q, want empty", gotSuffix)
	}
}

// TestRenderFIM_ExcludesSystemAndInstruction asserts System/Instruction never
// leak into the FIM prompt — a FIM model takes no system role and doesn't
// need the chat append-contract text.
func TestRenderFIM_ExcludesSystemAndInstruction(t *testing.T) {
	p := prompt.Prompt{
		System:      "UNIQUE_SYSTEM_MARKER",
		Instruction: "UNIQUE_INSTRUCTION_MARKER",
		Context:     "Context:\n- cwd: /tmp\n\n",
		Prefix:      "git",
		Suffix:      "",
	}
	gotPrompt, _ := RenderFIM(p)
	if strings.Contains(gotPrompt, "UNIQUE_SYSTEM_MARKER") {
		t.Errorf("RenderFIM() prompt unexpectedly contains System text: %q", gotPrompt)
	}
	if strings.Contains(gotPrompt, "UNIQUE_INSTRUCTION_MARKER") {
		t.Errorf("RenderFIM() prompt unexpectedly contains Instruction text: %q", gotPrompt)
	}
}

func TestFirstShellCommand(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain command unchanged", "git status", "git status"},
		{"leading semicolon stripped", "; source .venv/bin/activate", "source .venv/bin/activate"},
		{"leading &&  stripped", "&& make build", "make build"},
		{"leading separator with extra space", ";   cd ..", "cd .."},
		{"repeated leading separators", "; ; source x", "source x"},
		{"trailing chain cut at semicolon", "mkdir x; cd x; git init", "mkdir x"},
		{"trailing chain cut at &&", "git add . && git commit", "git add ."},
		{"cut at earliest separator", "a && b; c", "a"},
		{"pipe left intact", "ps aux | grep foo", "ps aux | grep foo"},
		{"leading strip then trailing cut", "; mkdir x; cd x", "mkdir x"},
		{"trailing whitespace trimmed", "ls -la   ", "ls -la"},
		{"empty stays empty", "", ""},
		{"only a separator becomes empty", ";", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := firstShellCommand(tt.in); got != tt.want {
				t.Errorf("firstShellCommand(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
