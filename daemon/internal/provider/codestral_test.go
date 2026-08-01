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
	want := "# cwd: /Users/x/proj\n# git: branch main (dirty)\n$ git com"
	if gotPrompt != want {
		t.Errorf("RenderFIM() prompt = %q, want %q", gotPrompt, want)
	}
	if gotSuffix != "" {
		t.Errorf("RenderFIM() suffix = %q, want empty", gotSuffix)
	}
}

// TestRenderFIM_PromptMarkerIsTheDefault is the A9 regression guard on the
// shipped shape: "$ " on every history line AND on the cursor line, ambient
// comments left unmarked (they are not commands), history/cursor still
// contiguous. The cursor line must NOT be a bare newline — that shape is
// exactly what let the model continue "git push".
func TestRenderFIM_PromptMarkerIsTheDefault(t *testing.T) {
	p := prompt.Prompt{
		Context: "Context:\n- cwd: /x/proj\n\n",
		History: []string{"git status", "git push"},
		Prefix:  "",
	}
	gotPrompt, _ := RenderFIM(p)
	want := "# cwd: /x/proj\n$ git status\n$ git push\n$ "
	if gotPrompt != want {
		t.Errorf("RenderFIM() = %q, want %q", gotPrompt, want)
	}
	if strings.HasSuffix(gotPrompt, "git push\n") {
		t.Error("RenderFIM() ends right after the last history line — the A9 shape")
	}
}

// TestRenderFIM_TypingKeepsPrefixOnMarkedLine confirms the marker precedes a
// non-empty buffer too, so typing mode sees the same transcript shape rather
// than a bare line.
func TestRenderFIM_TypingKeepsPrefixOnMarkedLine(t *testing.T) {
	p := prompt.Prompt{History: []string{"git status"}, Prefix: "git com"}
	gotPrompt, _ := RenderFIM(p)
	want := "$ git status\n$ git com"
	if gotPrompt != want {
		t.Errorf("RenderFIM() = %q, want %q", gotPrompt, want)
	}
}

// TestRenderFIMNoPromptMarker pins the pre-A9 baseline shape, kept so the
// marker decision stays measurable.
func TestRenderFIMNoPromptMarker(t *testing.T) {
	p := prompt.Prompt{History: []string{"git status", "git push"}}
	gotPrompt, _ := RenderFIMNoPromptMarker(p)
	want := "git status\ngit push\n"
	if gotPrompt != want {
		t.Errorf("RenderFIMNoPromptMarker() = %q, want %q", gotPrompt, want)
	}
}

// TestRenderFIMExitCodeAlways pins that the exit line lands BETWEEN history
// and the cursor (not up with the ambient comments) and is emitted at 0,
// which prompt.contextBlock omits. It builds on the shipped shape, so the
// prompt marker must still be present — this variant differs from
// production in exactly one thing.
func TestRenderFIMExitCodeAlways(t *testing.T) {
	p := prompt.Prompt{
		Context:  "Context:\n- cwd: /x/proj\n\n",
		History:  []string{"git status", "git push"},
		LastExit: 0,
	}
	gotPrompt, _ := RenderFIMExitCodeAlways(p)
	want := "# cwd: /x/proj\n$ git status\n$ git push\n# exit: 0\n$ "
	if gotPrompt != want {
		t.Errorf("RenderFIMExitCodeAlways() = %q, want %q", gotPrompt, want)
	}
}

func TestRenderFIMExitCodeAlways_NonZero(t *testing.T) {
	p := prompt.Prompt{History: []string{"go build ./..."}, LastExit: 1}
	gotPrompt, _ := RenderFIMExitCodeAlways(p)
	want := "$ go build ./...\n# exit: 1\n$ "
	if gotPrompt != want {
		t.Errorf("RenderFIMExitCodeAlways() = %q, want %q", gotPrompt, want)
	}
}

// TestWithFIMRenderer confirms the option actually reaches Complete's
// rendering path (observed through RenderPrompt, which uses the same
// c.render), and that a nil renderer leaves the shipped default in place
// rather than producing an empty prompt.
func TestWithFIMRenderer(t *testing.T) {
	req := Request{Prompt: prompt.Prompt{History: []string{"git push"}}}

	custom := newCodestralWith(t, WithFIMRenderer(RenderFIMNoPromptMarker))
	if got, want := custom.RenderPrompt(req), "git push\n"; got != want {
		t.Errorf("with custom renderer: RenderPrompt() = %q, want %q", got, want)
	}

	nilOpt := newCodestralWith(t, WithFIMRenderer(nil))
	if got, want := nilOpt.RenderPrompt(req), "$ git push\n$ "; got != want {
		t.Errorf("with nil renderer: RenderPrompt() = %q, want the default %q", got, want)
	}
}

func newCodestralWith(t *testing.T, opts ...CodestralOption) Provider {
	t.Helper()
	p, err := NewCodestral("http://unused", "test-model", "test-key", 48, opts...)
	if err != nil {
		t.Fatalf("NewCodestral() err = %v, want nil", err)
	}
	return p
}

// TestCodestral_RenderPrompt pins the two RenderPrompt shapes: with the
// Phase-2 empty suffix (today's only real case), the output is just the FIM
// prompt with no SUFFIX: section; a non-empty suffix (the FIM infill hook,
// unused today) adds one.
func TestCodestral_RenderPrompt(t *testing.T) {
	client := newCodestral(t, "http://unused", "test-model", "test-key", 48)

	t.Run("empty suffix", func(t *testing.T) {
		req := Request{Prompt: prompt.Prompt{Prefix: "git com"}}
		got := client.RenderPrompt(req)
		if got != "$ git com" {
			t.Errorf("RenderPrompt() = %q, want %q (no SUFFIX: section)", got, "$ git com")
		}
	})

	t.Run("non-empty suffix", func(t *testing.T) {
		req := Request{Prompt: prompt.Prompt{Prefix: "git com", Suffix: "mit"}}
		got := client.RenderPrompt(req)
		want := "PROMPT:\n$ git com\n\nSUFFIX:\nmit"
		if got != want {
			t.Errorf("RenderPrompt() = %q, want %q", got, want)
		}
	})
}

// TestRenderFIM_HistoryRenderedRaw pins the FIM raw-history contract: History
// entries are rendered as raw, uncommented command lines (not "#"-prefixed),
// contiguous with the buffer, and the ambient context block's own
// recent-commands line is skipped (not double-rendered as a comment).
// Ordering must be ambient-comments -> raw-history -> prefix.
func TestRenderFIM_HistoryRenderedRaw(t *testing.T) {
	p := prompt.Prompt{
		Context: "Context:\n- cwd: /Users/x/project\n- git: branch main (dirty)\n- last command failed (exit 1)\n- recent commands: git add .; git commit -m \"wip\"; git status\n\n",
		Prefix:  "git com",
		History: []string{"git add .", "git commit -m \"wip\"", "git status"},
	}
	gotPrompt, gotSuffix := RenderFIM(p)
	want := "# cwd: /Users/x/project\n# git: branch main (dirty)\n# last command failed (exit 1)\n" +
		"$ git add .\n$ git commit -m \"wip\"\n$ git status\n" +
		"$ git com"
	if gotPrompt != want {
		t.Errorf("RenderFIM() prompt = %q, want %q", gotPrompt, want)
	}
	if gotSuffix != "" {
		t.Errorf("RenderFIM() suffix = %q, want empty", gotSuffix)
	}
	if strings.Contains(gotPrompt, "# recent commands") {
		t.Errorf("RenderFIM() prompt unexpectedly comments the recent-commands line: %q", gotPrompt)
	}
}

// TestRenderFIM_NoHistoryJustAmbientContext checks that with no history but
// present ambient context, output is just the comment lines plus the prefix
// (no stray blank raw-history section).
func TestRenderFIM_NoHistoryJustAmbientContext(t *testing.T) {
	p := prompt.Prompt{
		Context: "Context:\n- cwd: /tmp\n\n",
		Prefix:  "git com",
		History: nil,
	}
	gotPrompt, _ := RenderFIM(p)
	want := "# cwd: /tmp\n$ git com"
	if gotPrompt != want {
		t.Errorf("RenderFIM() prompt = %q, want %q", gotPrompt, want)
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
	if gotPrompt != "$ git com" {
		t.Errorf("RenderFIM() prompt = %q, want %q", gotPrompt, "$ git com")
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

// TestFirstShellCommand is table-driven with mode as an explicit column so
// the two behaviours (typing preserves a single leading space; next-command
// strips it) sit side by side, alongside all the mode-INDEPENDENT behaviour
// (separator stripping, chain cutoff, plain pass-through) which each get one
// row per mode to prove a future change can't accidentally make them
// mode-dependent.
//
// This is also the regression test for the bug the eval harness caught:
// firstShellCommand used to TrimLeft every completion unconditionally, so a
// completion beginning with the space the system prompt explicitly asks for
// ("Begin with a space when the completion starts a new word or argument")
// could never reach the user — "git add" + " ." shipped as "git add."
// instead of "git add .". The fix is mode-dependent: typing mode (non-empty
// buffer) must preserve exactly one leading space; next-command mode (empty
// buffer) must still strip it, both because the suggestion IS the whole
// command there and because zsh's HIST_IGNORE_SPACE silently drops
// space-prefixed commands from history.
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

// TestComplete_Codestral_LeadingSpaceByMode drives Complete end to end (not
// just firstShellCommand directly) to prove req.Prompt.Prefix is what
// actually selects typing vs next-command mode: a non-empty Prefix must
// preserve the model's leading space, an empty Prefix must strip it.
func TestComplete_Codestral_LeadingSpaceByMode(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
		want   string
	}{
		{"non-empty prefix (typing mode) preserves leading space", "git add", " ."},
		{"empty prefix (next-command mode) strips leading space", "", "."},
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
			got, err := client.Complete(context.Background(), Request{Prompt: prompt.Prompt{Prefix: tt.prefix}, MaxTokens: 48})
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

// TestComplete_Codestral_SeparatorCutoff proves the streaming separator cutoff:
// once "mkdir x;" has arrived, Complete returns "mkdir x" without waiting for a
// deliberately slow trailing chain chunk — the early-stop that keeps chaining
// from costing stream time, distinct from the newline cutoff.
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
	got, err := client.Complete(context.Background(), Request{Prompt: prompt.Prompt{Prefix: "mkdir"}, MaxTokens: 48})
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
