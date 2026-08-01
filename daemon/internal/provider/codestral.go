// codestral.go is a hand-rolled client for Mistral's Codestral FIM
// (fill-in-the-middle) endpoint (design §6, §13 Phase 2). Codestral is a
// base completion model, not an instruct/chat model, so it takes a
// prompt+suffix request shape instead of messages — that different shape is
// why this is its own adapter rather than another OpenAI-compatible base URL.
// No SDK/deps: Mistral ships no official Go client.
package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/naasanov/zsh-autopilot/daemon/internal/prompt"
)

// fimStopSequences halt generation early. Only the newline is used: it keeps
// a code model from spilling onto a second line, and the shared accumulator
// cuts there too. Command-chaining (";"/"&&") is deliberately NOT a stop
// sequence — see firstShellCommand for why: a code model predicting a next
// command often *leads* with a separator ("; source .venv/bin/activate"), and
// a ";" stop would halt at that leading char and return empty. Chains are
// handled by post-processing instead, which a leading separator can't defeat.
var fimStopSequences = []string{"\n"}

// firstShellCommand extracts the first command from a raw FIM completion.
// Codestral is a code model and, left to its own devices, does two unwanted
// things for a single-suggestion ghost-text UX:
//
//   - leads a next-command prediction with a separator, e.g.
//     "; source .venv/bin/activate" — natural in code ("<cmd>; <next>"), but
//     here the buffer is empty so the ";" is spurious;
//   - chains commands on one line, e.g. "mkdir x; cd y; git init".
//
// So: strip any leading separators/whitespace, then keep only up to the first
// top-level ";" or "&&". Pipes ("|") are left intact — they're within one
// command (`ps aux | grep`), not a chain. This runs on the accumulated text
// rather than as a stop sequence precisely so a leading separator becomes the
// real command instead of nuking the whole suggestion.
//
// typing selects which whitespace rule applies (see stripLeadingSeparators):
// pass true when completing a non-empty buffer (the system prompt tells the
// model to lead with a space to separate words, and that space must survive),
// false for a next-command prediction against an empty buffer (no word to
// separate from, and a leading space would silently keep the command out of
// zsh history under HIST_IGNORE_SPACE).
func firstShellCommand(s string, typing bool) string {
	s = stripLeadingSeparators(s, typing)
	if i := indexSeparator(s); i >= 0 {
		s = s[:i]
	}
	return strings.TrimRight(s, " \t")
}

// firstCommandComplete reports whether s already holds a complete first
// command followed by a separator — meaning we can stop reading the stream
// early, because everything past that separator would be discarded by
// firstShellCommand anyway. Leading separators are stripped first so a
// next-command prediction that opens with "; " is NOT treated as complete
// before its real command has streamed in (that was the empty-output bug the
// ";" stop sequence caused). Returns false while only a leading separator has
// arrived, or while the first command is still streaming.
func firstCommandComplete(s string) bool {
	// The mode argument only changes whether a single leading space survives
	// when NO separator is found at all (see stripLeadingSeparators) — it
	// never changes whether a separator is found, which is all this cares
	// about. So the mode passed here is arbitrary; false is picked for no
	// particular reason.
	return indexSeparator(stripLeadingSeparators(s, false)) >= 0
}

// stripLeadingSeparators removes any run of leading ";"/"&&" separators (a
// code model tends to open a next-command prediction with one), along with
// all whitespace immediately touching a removed separator — that whitespace
// belongs to the separator's own formatting ("; cmd", " && cmd"), not to the
// completion text, so it is always fully discarded regardless of mode.
//
// What's left, if no separator was ever found, is s's original leading
// whitespace run (if any). That run is a DIFFERENT thing: the word-separator
// space the system prompt tells the model to lead with when completing a
// non-empty buffer ("Begin with a space when the completion starts a new
// word or argument", see prompt.go). So its handling is mode-dependent:
//
//   - typing == true (non-empty buffer): collapse the run to exactly one
//     leading space — a model that emits "   ." should still produce " .",
//     not "git add   .", but the single space must survive or the shipped
//     suggestion collides with the buffer ("git add" + "." -> "git add.",
//     the exact bug this function exists to prevent).
//   - typing == false (empty buffer, next-command mode): strip it entirely.
//     The suggestion IS the whole command; a leading space is pure noise,
//     and under zsh's HIST_IGNORE_SPACE a command that starts with a space
//     is silently kept out of history — a real user-visible defect.
//
// Shared by firstShellCommand and firstCommandComplete so both treat the
// leading run identically.
func stripLeadingSeparators(s string, typing bool) string {
	foundSeparator := false
	cur := s
	for {
		trimmed := strings.TrimLeft(cur, " \t")
		next := strings.TrimPrefix(strings.TrimPrefix(trimmed, "&&"), ";")
		if next == trimmed {
			break
		}
		foundSeparator = true
		cur = next
	}

	// A separator was stripped (or the caller is in next-command mode):
	// discard any remaining leading whitespace entirely.
	if foundSeparator || !typing {
		return strings.TrimLeft(cur, " \t")
	}

	// No separator anywhere: cur == s. Preserve at most one leading space,
	// the word-separator space, in typing mode.
	trimmed := strings.TrimLeft(cur, " \t")
	if cur == trimmed {
		return cur // no leading whitespace to begin with
	}
	if trimmed == "" {
		return cur // whitespace-only input: nothing meaningful to preserve
	}
	return " " + trimmed
}

// indexSeparator returns the byte index of the first ";" or "&&" in s, or -1.
func indexSeparator(s string) int {
	semi := strings.IndexByte(s, ';')
	and := strings.Index(s, "&&")
	switch {
	case semi < 0:
		return and
	case and < 0:
		return semi
	default:
		return min(semi, and)
	}
}

// codestralClient talks to a single Mistral-compatible /v1/fim/completions
// endpoint. It holds a shared *http.Client so the daemon never pays TLS/TCP
// setup cost per keystroke (design §4 "warm connections") — construct one via
// NewCodestral at startup and reuse it for every request.
type codestralClient struct {
	baseURL   string
	model     string
	apiKey    string
	maxTokens int
	stop      []string
	http      *http.Client
	render    FIMRenderer
}

// FIMRenderer turns a provider-neutral Prompt into the prompt+suffix pair a
// FIM endpoint takes. RenderFIM is the shipped default; the type exists so
// the eval harness can measure alternative prompt SHAPES (not just alternate
// Prompt contents, which the Variant.Build seam already covers) without
// those experiments living in production code paths. See WithFIMRenderer.
type FIMRenderer func(prompt.Prompt) (fimPrompt, suffix string)

// CodestralOption customizes a codestral client at construction. Variadic
// options rather than more positional params: the only current knob is
// eval-only, and it must not push itself into every production call site.
type CodestralOption func(*codestralClient)

// WithFIMRenderer replaces the FIM prompt renderer. Production passes
// nothing and gets RenderFIM; the eval harness passes an experimental shape
// (see eval.Variant.FIMRenderer) so a prompt-shape hypothesis can be scored
// against the default across the whole corpus. A nil renderer is ignored, so
// a zero-value/unset option can't silently produce an empty prompt.
func WithFIMRenderer(r FIMRenderer) CodestralOption {
	return func(c *codestralClient) {
		if r != nil {
			c.render = r
		}
	}
}

// NewCodestral builds a Provider backed by a shared, keep-alive-tuned
// http.Client. Defaults: baseURL "https://api.mistral.ai", model
// "codestral-latest" — the design originally named codestral.mistral.ai, but
// a pay-as-you-go Mistral account cannot mint a Codestral-specific key, so
// the general per-token endpoint (which serves the same FIM endpoint under a
// regular Mistral key) is the default. baseURL stays configurable so
// codestral.mistral.ai still works for anyone holding that key.
func NewCodestral(baseURL, model, apiKey string, maxTokens int, opts ...CodestralOption) (Provider, error) {
	if baseURL == "" {
		baseURL = "https://api.mistral.ai"
	}
	if model == "" {
		model = "codestral-latest"
	}
	c := &codestralClient{
		baseURL:   strings.TrimRight(baseURL, "/"),
		model:     model,
		apiKey:    apiKey,
		maxTokens: maxTokens,
		stop:      fimStopSequences,
		http:      keepAliveHTTPClient(),
		render:    RenderFIM,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// Name identifies this adapter for METRICS(§12) and price-table lookups.
func (c *codestralClient) Name() string {
	return "codestral"
}

// Model returns the model name this client was constructed with.
//
// METRICS(§12): used by internal/suggest to look up pricing for the
// "request" event's cost_usd field.
func (c *codestralClient) Model() string {
	return c.model
}

// RenderPrompt returns the exact FIM prompt(+suffix) Complete would send.
// Suffix is "" in Phase 2 (see RenderFIM's doc comment), so this is
// ordinarily just the FIM prompt; the SUFFIX: section only appears once the
// FIM infill hook is actually used.
func (c *codestralClient) RenderPrompt(req Request) string {
	fimPrompt, suffix := c.render(req.Prompt)
	if suffix == "" {
		return fimPrompt
	}
	return "PROMPT:\n" + fimPrompt + "\n\nSUFFIX:\n" + suffix
}

// RenderFIM renders a Prompt for a FIM endpoint. FIM models take no system
// role, so ambient context (cwd/files/git/last-exit) is encoded as shell
// comment lines above the buffer, same as before.
//
// Recent shell history gets different treatment: it is rendered as RAW,
// uncommented command lines contiguous with the buffer, not as a "#"
// comment. Codestral is a base code-completion model — it continues
// in-distribution code far better than it "reasons" over commented metadata,
// and shell history entries are themselves valid shell commands, i.e. real
// code. So the rendered prompt looks like an actual shell session ("git add
// .\ngit commit -m \"wip\"\ngit status\n" + buffer) rather than a comment
// block, priming the model to continue the session instead of summarizing
// it. The ambient context block's own "recent commands" line (from
// prompt.contextBlock, labeled via prompt.RecentCommandsLabel) is skipped
// here to avoid rendering the same req.History data twice.
//
// Each command line — history and the cursor line alike — is prefixed with a
// "$ " prompt marker, so the whole thing reads as a shell TRANSCRIPT. That
// marker is load-bearing, not decoration: without it the model completed the
// LAST HISTORY LINE instead of predicting a new command ("git push" ->
// "origin master", eval case A9). A trailing newline alone did NOT fix that —
// the rendered prompt already ended in one — because either the endpoint
// trims trailing whitespace off `prompt` or the model simply reads "git push"
// as a prefix of "git push origin main". A non-whitespace marker is what
// makes "a new command starts here" structural rather than positional.
// Measured: the marker shape beat both the unmarked default and the
// "# exit: N" boundary across the corpus, on codestral.
//
// Suffix is "" today, making this pure prefix continuation — exactly what
// FIM models are trained to nail.
func RenderFIM(p prompt.Prompt) (fimPrompt, suffix string) {
	return renderFIMShape(p, defaultFIMShape())
}

// defaultPromptMarker is the shipped transcript marker. A trailing space is
// part of it: it separates the marker from the command, and in next-command
// mode it means the FIM prompt ends "$ ", putting the cursor where a real
// shell would. Any leading space the model emits in response is stripped by
// stripLeadingSeparators in next-command mode, so a marker-induced space can
// never reach the suggestion.
const defaultPromptMarker = "$ "

// defaultFIMShape is the SHIPPED shape. Experimental shapes are expressed as
// deltas from this (start here, change one thing) rather than from the zero
// value — otherwise a variant would silently also revert whatever the
// default has since adopted, and measure two changes while claiming one.
func defaultFIMShape() fimShape {
	return fimShape{promptMarker: defaultPromptMarker}
}

// fimShape is the set of knobs renderFIMShape varies. The zero value is NOT
// the shipped default (see defaultFIMShape) — it is the pre-A9 unmarked
// shape, kept reachable only so the eval harness can still measure against
// it. Nothing here changes production behavior without a caller opting in
// via WithFIMRenderer.
type fimShape struct {
	// promptMarker, when non-empty, prefixes every raw history line AND is
	// emitted once more immediately before Prefix, so the cursor sits after
	// it. See RenderFIM for why this is the shipped default.
	promptMarker string

	// alwaysExitCode emits a "# exit: N" comment line between the history
	// block and Prefix even when N is 0 — which contextBlock deliberately
	// omits. Measured and NOT adopted: it lost to promptMarker, and it can't
	// separate its two effects anyway (real signal vs. acting as a
	// non-whitespace boundary), while costing tokens on every request to
	// restate "the last command succeeded".
	alwaysExitCode bool
}

func renderFIMShape(p prompt.Prompt, shape fimShape) (fimPrompt, suffix string) {
	// p.Context is pre-rendered like "Context:\n- cwd: ...\n- git: ...\n\n"
	// (prompt.contextBlock). Strip the "Context:" header and the trailing
	// blank-line separator, then re-render each remaining "- " line as a "#"
	// shell comment, EXCEPT the recent-commands line, which is rendered raw
	// (below) instead. p.System and p.Instruction are chat-model
	// append-contract text a FIM model neither needs nor benefits from, so
	// they are deliberately excluded.
	ctx := strings.TrimPrefix(p.Context, "Context:\n")
	ctx = strings.TrimSuffix(ctx, "\n\n")

	var b strings.Builder
	if ctx != "" {
		for line := range strings.SplitSeq(ctx, "\n") {
			stripped := strings.TrimPrefix(line, "- ")
			if strings.HasPrefix(stripped, prompt.RecentCommandsLabel) {
				continue
			}
			b.WriteString("# ")
			b.WriteString(stripped)
			b.WriteString("\n")
		}
	}
	// History as raw command lines, oldest-first, contiguous with the
	// buffer — no "#" prefix, so the model sees a real shell session to
	// continue rather than commented-out metadata.
	for _, cmd := range p.History {
		b.WriteString(shape.promptMarker)
		b.WriteString(cmd)
		b.WriteString("\n")
	}
	// The exit-status line goes AFTER history and BEFORE the buffer on
	// purpose: its job in this shape is to sit between the last command and
	// the cursor. Putting it up with the other ambient comments (where
	// contextBlock renders it for chat) would leave the history/cursor seam
	// exactly as bare as it is today.
	if shape.alwaysExitCode {
		b.WriteString("# exit: ")
		b.WriteString(strconv.Itoa(p.LastExit))
		b.WriteString("\n")
	}
	// Buffer goes last, with no trailing newline, so the model's completion
	// continues directly from it. The marker is written even when Prefix is
	// empty — that is precisely the next-command case A9 covers, where the
	// marker is the only thing telling the model a fresh command starts here.
	b.WriteString(shape.promptMarker)
	b.WriteString(p.Prefix)
	return b.String(), p.Suffix
}

// RenderFIMNoPromptMarker renders the PRE-A9 shape: raw history lines and a
// bare cursor line, with no "$ " transcript marker.
//
// This is what RenderFIM used to do, kept reachable so the change that
// replaced it stays measurable — a default with no way to A/B against its
// predecessor can only be re-litigated by hand-editing production code. It
// is the baseline, not a candidate: it is known to lose (it is the shape
// that produced A9).
func RenderFIMNoPromptMarker(p prompt.Prompt) (fimPrompt, suffix string) {
	return renderFIMShape(p, fimShape{})
}

// RenderFIMExitCodeAlways renders the shipped shape PLUS an "# exit: N"
// comment line between the history block and the cursor, emitted even when N
// is 0 (contextBlock omits it there).
//
// Deliberately built on defaultFIMShape, so it keeps the prompt marker and
// differs from production in exactly one thing. Measured and not adopted; it
// stays registered because "does explicit exit status help now that the
// boundary problem is solved" is a different question from the one it
// originally lost, and re-running it is cheaper than rebuilding it.
func RenderFIMExitCodeAlways(p prompt.Prompt) (fimPrompt, suffix string) {
	shape := defaultFIMShape()
	shape.alwaysExitCode = true
	return renderFIMShape(p, shape)
}

// fimRequest mirrors just the subset of the Mistral FIM request schema this
// client needs. Note prompt/suffix, NOT messages — the shape that makes this
// its own adapter rather than another OpenAI-compatible chat endpoint.
type fimRequest struct {
	Model     string   `json:"model"`
	Prompt    string   `json:"prompt"`
	Suffix    string   `json:"suffix"`
	MaxTokens int      `json:"max_tokens"`
	Stream    bool     `json:"stream"`
	Stop      []string `json:"stop,omitempty"`
}

// fimChunk mirrors one SSE "data:" event's JSON payload. Codestral's FIM
// streaming response is OpenAI-shaped (choices[0].delta.content), so this is
// the same shape as openai.go's streamChunk.
type fimChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		// METRICS(§12): finish_reason ("stop", "length", ...) is set on the
		// last per-choice chunk.
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	// METRICS(§12): usage arrives on its own final chunk (choices may be
	// empty on that chunk). Decoded defensively — absent/zero is fine and
	// expected whenever the first-line cutoff returns before this chunk
	// arrives.
	Usage *fimUsage `json:"usage"`
}

// METRICS(§12): fimUsage mirrors the OpenAI-compatible usage object.
type fimUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	PromptTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

// Complete issues a streaming FIM completions request and returns the
// model's first line of output (design §4 "stream + take first line only"),
// driving the shared accumulator for TTFT stamping and the cutoff.
//
// It honors ctx throughout: the HTTP request itself is built with
// NewRequestWithContext, so cancelling ctx (e.g. because a newer keystroke
// superseded this request — see server.handle) aborts the call, including
// mid-stream reads of the response body.
func (c *codestralClient) Complete(ctx context.Context, req Request) (Completion, error) {
	// typing mirrors the system prompt's own mode split (prompt.go): a
	// non-empty buffer is a typing-mode completion, which must preserve the
	// model's leading word-separator space; an empty buffer is a
	// next-command prediction, which must not. See stripLeadingSeparators.
	typing := req.Prompt.Prefix != ""

	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = c.maxTokens
	}
	fimPrompt, suffix := c.render(req.Prompt)
	reqBody := fimRequest{
		Model:     c.model,
		Prompt:    fimPrompt,
		Suffix:    suffix,
		MaxTokens: maxTokens,
		Stream:    true,
		Stop:      c.stop,
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return Completion{}, &Error{Kind: ErrTransport, Provider: c.Name(), Err: fmt.Errorf("provider: marshal request: %w", err)}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/fim/completions", bytes.NewReader(payload))
	if err != nil {
		return Completion{}, &Error{Kind: ErrTransport, Provider: c.Name(), Err: fmt.Errorf("provider: build request: %w", err)}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Accept", "text/event-stream")

	// METRICS(§12): TTFT is measured from just before the round trip starts
	// to the first chunk carrying non-empty delta content (accumulator.Push).
	acc := newAccumulator(time.Now())

	resp, err := c.http.Do(httpReq)
	if err != nil {
		// Covers network errors AND ctx cancellation/deadline (http.Client.Do
		// returns ctx.Err(), possibly wrapped, when ctx ends before or during
		// the round trip).
		if ctx.Err() != nil {
			return Completion{}, &Error{Kind: ErrCanceled, Provider: c.Name(), Err: err}
		}
		return Completion{}, &Error{Kind: ErrTransport, Provider: c.Name(), Err: fmt.Errorf("provider: request: %w", err)}
	}
	// Closing the body (a) prevents leaking the connection on every return
	// path below, including the early "first newline seen" cutoff, and (b)
	// is what actually aborts the in-progress read on a slow/blocked stream
	// once ctx is cancelled or we simply stop caring.
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return Completion{HTTPStatus: resp.StatusCode}, &Error{
			Kind:       ClassifyHTTP(resp.StatusCode),
			HTTPStatus: resp.StatusCode,
			Provider:   c.Name(),
			Err:        fmt.Errorf("provider: unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(body))),
		}
	}

	// SSE parsing: the response body is a sequence of lines. Each event we
	// care about looks like "data: {...json...}\n"; a blank line separates
	// events but bufio.Scanner's default ScanLines split already gives us
	// one line at a time, so we just filter for the "data:" prefix and skip
	// everything else (blank keep-alive lines, comments, other fields).
	var stopReason string
	var inputTokens, outputTokens, cachedTokens int
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			break
		}

		var chunk fimChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return Completion{HTTPStatus: resp.StatusCode}, &Error{Kind: ErrTransport, HTTPStatus: resp.StatusCode, Provider: c.Name(), Err: fmt.Errorf("provider: malformed stream chunk: %w", err)}
		}

		// METRICS(§12): usage lands on its own final chunk (choices may be
		// empty there), so this check must happen before the len==0 skip
		// below.
		if chunk.Usage != nil {
			inputTokens = chunk.Usage.PromptTokens
			outputTokens = chunk.Usage.CompletionTokens
			if chunk.Usage.PromptTokensDetails != nil {
				cachedTokens = chunk.Usage.PromptTokensDetails.CachedTokens
			}
		}
		if len(chunk.Choices) == 0 {
			continue
		}

		if chunk.Choices[0].FinishReason != "" {
			stopReason = chunk.Choices[0].FinishReason
		}

		// Two cutoffs stop the read early (design §4 — the point of streaming):
		//
		//  1. Newline (shared accumulator): a complete single-line suggestion.
		//  2. Shell separator (firstCommandComplete, codestral-specific): the
		//     first command plus a trailing ";"/"&&" has arrived, so the rest
		//     of the chain is about to be discarded by firstShellCommand
		//     anyway — stop now rather than stream it. This is what keeps a
		//     code model's chaining ("mkdir x; cd y; ...") from costing the
		//     ~66ms p50 / ~490ms p95 of stream time it did before, WITHOUT the
		//     empty-output bug the ";" stop sequence had: a leading "; " is
		//     stripped first, so we wait for the real command before stopping.
		//
		// METRICS(§12) note: returning here precedes the trailing usage chunk,
		// so InputTokens/OutputTokens/CachedTokens are typically zero on this
		// path — expected, and not worth chasing at the cost of the cutoff.
		stop := acc.Push(chunk.Choices[0].Delta.Content)
		if !stop && firstCommandComplete(acc.Raw()) {
			stop = true
		}
		if stop {
			return Completion{
				Text:         firstShellCommand(acc.Text(), typing),
				TTFT:         acc.TTFT(),
				InputTokens:  inputTokens,
				OutputTokens: outputTokens,
				CachedTokens: cachedTokens,
				HTTPStatus:   resp.StatusCode,
				StopReason:   stopReason,
			}, nil
		}
	}

	if serr := scanner.Err(); serr != nil {
		// Prefer ctx.Err() when set: a cancelled/expired ctx is what actually
		// aborted the read, and scanner.Err() would otherwise surface as an
		// opaque wrapped "context canceled" from the transport anyway.
		if ctx.Err() != nil {
			return Completion{HTTPStatus: resp.StatusCode}, &Error{Kind: ErrCanceled, HTTPStatus: resp.StatusCode, Provider: c.Name(), Err: ctx.Err()}
		}
		return Completion{HTTPStatus: resp.StatusCode}, &Error{Kind: ErrTransport, HTTPStatus: resp.StatusCode, Provider: c.Name(), Err: fmt.Errorf("provider: read stream: %w", serr)}
	}
	if ctx.Err() != nil {
		return Completion{HTTPStatus: resp.StatusCode}, &Error{Kind: ErrCanceled, HTTPStatus: resp.StatusCode, Provider: c.Name(), Err: ctx.Err()}
	}

	// Stream ended (EOF / [DONE] / max_tokens finish) with no newline seen:
	// return whatever we accumulated as the whole (single-line) completion.
	return Completion{
		Text:         firstShellCommand(acc.Text(), typing),
		TTFT:         acc.TTFT(),
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		CachedTokens: cachedTokens,
		HTTPStatus:   resp.StatusCode,
		StopReason:   stopReason,
	}, nil
}
