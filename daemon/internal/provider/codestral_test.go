package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/naasanov/zsh-autopilot/daemon/internal/prompt"
	"github.com/naasanov/zsh-autopilot/daemon/internal/protocol"
)

// shippedFIMPrompt is the prompt every test constructs a codestralClient
// with — RenderFIM's own shape (comments/history/marker) is prompt
// package's responsibility and tested there, not here.
func shippedFIMPrompt(t *testing.T) prompt.FIMPrompt {
	t.Helper()
	p, err := prompt.ByName("fim-transcript-marker")
	if err != nil {
		t.Fatalf("prompt.ByName(fim-transcript-marker): %v", err)
	}
	fp, ok := p.(prompt.FIMPrompt)
	if !ok {
		t.Fatalf("prompt %q is not FIM-shaped", p.Name())
	}
	return fp
}

func newCodestral(t *testing.T, baseURL, model, apiKey string, maxTokens int) Provider {
	t.Helper()
	p, err := NewCodestral(baseURL, model, apiKey, maxTokens, shippedFIMPrompt(t))
	if err != nil {
		t.Fatalf("NewCodestral() err = %v, want nil", err)
	}
	return p
}

// fimSSEChunk builds one SSE "data:" line carrying content as a FIM
// completions delta, matching fimChunk's (OpenAI-shaped) response.
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
	req := Request{Req: protocol.Request{Buf: "git"}, MaxTokens: 48}
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
	if gotBody.Prompt != "$ git" {
		t.Errorf("request Prompt = %q, want %q", gotBody.Prompt, "$ git")
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

// TestComplete_Codestral_FirstLineCutoff: content spans a newline partway
// through, with a deliberately slow final chunk. Asserts only the text before
// the newline comes back, and Complete returns before the final chunk arrives.
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
	got, err := client.Complete(context.Background(), Request{Req: protocol.Request{Buf: "user"}, MaxTokens: 48})
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

// TestComplete_Codestral_Cancellation: a stream blocks indefinitely after its
// first chunk; cancelling ctx must abort it promptly with a context error.
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
		_, err := client.Complete(ctx, Request{Req: protocol.Request{Buf: "user"}, MaxTokens: 48})
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
			got, err := client.Complete(context.Background(), Request{Req: protocol.Request{Buf: "user"}, MaxTokens: 48})
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

// METRICS(§12): TestComplete_UsageAndFinishReason: a stream ends (no newline,
// so the cutoff doesn't fire) with a trailing usage chunk and finish_reason,
// asserting both decode onto the returned Completion.
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
	got, err := client.Complete(context.Background(), Request{Req: protocol.Request{Buf: "user"}, MaxTokens: 48})
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

// TestNewCodestral_Defaults asserts the empty-value defaults: baseURL
// "https://api.mistral.ai", model "codestral-latest".
func TestNewCodestral_Defaults(t *testing.T) {
	p, err := NewCodestral("", "", "test-key", 48, shippedFIMPrompt(t))
	if err != nil {
		t.Fatalf("NewCodestral() err = %v, want nil", err)
	}
	if p.Name() != "codestral" {
		t.Errorf("Name() = %q, want %q", p.Name(), "codestral")
	}
	if p.Model() != "codestral-latest" {
		t.Errorf("Model() = %q, want %q", p.Model(), "codestral-latest")
	}
	if p.PromptName() != "fim-transcript-marker" {
		t.Errorf("PromptName() = %q, want %q", p.PromptName(), "fim-transcript-marker")
	}
	c := p.(*codestralClient)
	if c.baseURL != "https://api.mistral.ai" {
		t.Errorf("baseURL = %q, want %q", c.baseURL, "https://api.mistral.ai")
	}
}

// TestCodestral_RenderPrompt pins the two RenderPrompt shapes: an empty
// suffix (today's only real case) omits the SUFFIX: section; a non-empty one
// (the unused FIM infill hook) adds it — protocol.Request carries no cursor
// position, so a non-empty suffix only ever comes from a prompt built for it.
func TestCodestral_RenderPrompt(t *testing.T) {
	client := newCodestral(t, "http://unused", "test-model", "test-key", 48)

	req := Request{Req: protocol.Request{Buf: "git com"}}
	got := client.RenderPrompt(req)
	want := "$ git com"
	if got != want {
		t.Errorf("RenderPrompt() = %q, want %q", got, want)
	}
}

// TestFirstShellCommand is table-driven with mode as an explicit column:
// typing mode must preserve exactly one leading space (the completion's
// word-separator space — stripping it unconditionally used to collapse
// "git add" + " ." into "git add."), while next-command mode must strip it
// (the suggestion IS the whole command, and zsh's HIST_IGNORE_SPACE silently
// drops space-prefixed commands from history). Separator stripping and chain
// cutoff are mode-independent and get one row per mode to prove it stays
// that way.
func TestFirstShellCommand(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		typing bool
		want   string
	}{
		// --- leading space, no separator: the mode-dependent bug fix ---
		{"typing: single leading space preserved", " .", true, " ."},
		{"next-command: leading space stripped", " .", false, "."},
		{"typing: no leading space unaffected", ".", true, "."},
		{"next-command: no leading space unaffected", ".", false, "."},
		{"typing: multiple leading spaces collapsed to one", "   .", true, " ."},
		{"next-command: multiple leading spaces stripped", "   .", false, "."},
		{"typing: leading space, longer completion", " git status", true, " git status"},
		{"next-command: leading space, longer completion", " git status", false, "git status"},

		// --- separator stripping: identical in both modes ---
		{"typing: leading semicolon stripped", "; source .venv/bin/activate", true, "source .venv/bin/activate"},
		{"next-command: leading semicolon stripped", "; source .venv/bin/activate", false, "source .venv/bin/activate"},
		{"typing: leading semicolon with leading space stripped", " ; source x", true, "source x"},
		{"next-command: leading semicolon with leading space stripped", " ; source x", false, "source x"},
		{"typing: leading && stripped", "&& cd y", true, "cd y"},
		{"next-command: leading && stripped", "&& cd y", false, "cd y"},
		{"typing: leading space then && stripped", " && cd y", true, "cd y"},
		{"next-command: leading space then && stripped", " && cd y", false, "cd y"},
		{"typing: leading separator with extra space", ";   cd ..", true, "cd .."},
		{"next-command: leading separator with extra space", ";   cd ..", false, "cd .."},
		{"typing: repeated leading separators", "; ; source x", true, "source x"},
		{"next-command: repeated leading separators", "; ; source x", false, "source x"},

		// --- chaining cutoff: identical in both modes ---
		{"typing: trailing chain cut at semicolon", "mkdir x; cd x; git init", true, "mkdir x"},
		{"next-command: trailing chain cut at semicolon", "mkdir x; cd x; git init", false, "mkdir x"},
		{"typing: trailing chain cut at &&", "git add . && git commit", true, "git add ."},
		{"next-command: trailing chain cut at &&", "git add . && git commit", false, "git add ."},
		{"typing: leading separator then chain", "; mkdir x && cd y", true, "mkdir x"},
		{"next-command: leading separator then chain", "; mkdir x && cd y", false, "mkdir x"},
		{"typing: cut at earliest separator", "a && b; c", true, "a"},
		{"next-command: cut at earliest separator", "a && b; c", false, "a"},

		// --- plain pass-through / edge cases: identical in both modes ---
		{"typing: plain command unchanged", "git status", true, "git status"},
		{"next-command: plain command unchanged", "git status", false, "git status"},
		{"typing: pipe left intact", "ps aux | grep foo", true, "ps aux | grep foo"},
		{"next-command: pipe left intact", "ps aux | grep foo", false, "ps aux | grep foo"},
		{"typing: trailing whitespace trimmed", "ls -la   ", true, "ls -la"},
		{"next-command: trailing whitespace trimmed", "ls -la   ", false, "ls -la"},
		{"typing: empty stays empty", "", true, ""},
		{"next-command: empty stays empty", "", false, ""},
		{"typing: whitespace-only becomes empty", "   ", true, ""},
		{"next-command: whitespace-only becomes empty", "   ", false, ""},
		{"typing: only a separator becomes empty", ";", true, ""},
		{"next-command: only a separator becomes empty", ";", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := firstShellCommand(tt.in, tt.typing); got != tt.want {
				t.Errorf("firstShellCommand(%q, typing=%v) = %q, want %q", tt.in, tt.typing, got, tt.want)
			}
		})
	}
}

// TestComplete_Codestral_LeadingSpaceByMode drives Complete end-to-end to
// prove req.Req.Buf selects typing vs next-command mode: non-empty preserves
// the model's leading space, empty strips it.
func TestComplete_Codestral_LeadingSpaceByMode(t *testing.T) {
	tests := []struct {
		name string
		buf  string
		want string
	}{
		{"non-empty buffer (typing mode) preserves leading space", "git add", " ."},
		{"empty buffer (next-command mode) strips leading space", "", "."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				flusher := w.(http.Flusher)
				fmt.Fprint(w, fimSSEChunk(t, " ."))
				flusher.Flush()
				fmt.Fprint(w, "data: [DONE]\n\n")
				flusher.Flush()
			}))
			defer srv.Close()

			client := newCodestral(t, srv.URL, "test-model", "test-key", 48)
			got, err := client.Complete(context.Background(), Request{Req: protocol.Request{Buf: tt.buf}, MaxTokens: 48})
			if err != nil {
				t.Fatalf("Complete() err = %v, want nil", err)
			}
			if got.Text != tt.want {
				t.Errorf("Complete().Text = %q, want %q", got.Text, tt.want)
			}
		})
	}
}

func TestFirstCommandComplete(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"still streaming first command", "source .venv/bin/act", false},
		{"leading separator only, no command yet", "; ", false},
		{"leading separator with partial command", "; source .venv", false},
		{"first command done, trailing semicolon", "mkdir x;", true},
		{"first command done, trailing &&", "git add .&&", true},
		{"leading sep then command then sep", "; source x;", true},
		{"pipe is not a boundary", "ps aux | grep foo", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := firstCommandComplete(tt.in); got != tt.want {
				t.Errorf("firstCommandComplete(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// TestComplete_Codestral_SeparatorCutoff: once "mkdir x;" has arrived,
// Complete returns "mkdir x" without waiting for a deliberately slow trailing
// chain chunk — the separator early-stop, distinct from the newline cutoff.
func TestComplete_Codestral_SeparatorCutoff(t *testing.T) {
	const lateDelay = 300 * time.Millisecond

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		fmt.Fprint(w, fimSSEChunk(t, "mkdir x"))
		flusher.Flush()
		fmt.Fprint(w, fimSSEChunk(t, "; "))
		flusher.Flush()

		// If consumed, this would chain more commands. The client must not
		// wait: the first command plus its separator already arrived.
		time.Sleep(lateDelay)
		fmt.Fprint(w, fimSSEChunk(t, "cd x; git init"))
		flusher.Flush()
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	client := newCodestral(t, srv.URL, "test-model", "test-key", 48)
	start := time.Now()
	got, err := client.Complete(context.Background(), Request{Req: protocol.Request{Buf: "mkdir"}, MaxTokens: 48})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Complete() err = %v, want nil", err)
	}
	if got.Text != "mkdir x" {
		t.Errorf("Complete().Text = %q, want %q", got.Text, "mkdir x")
	}
	if elapsed >= lateDelay {
		t.Errorf("Complete() took %v, expected to return before the %v late chunk (no separator cutoff)", elapsed, lateDelay)
	}
}
