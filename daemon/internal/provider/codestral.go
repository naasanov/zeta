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
func firstShellCommand(s string) string {
	s = stripLeadingSeparators(s)
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
	return indexSeparator(stripLeadingSeparators(s)) >= 0
}

// stripLeadingSeparators removes leading whitespace and any run of leading
// ";"/"&&" separators (a code model tends to open a next-command prediction
// with one). Shared by firstShellCommand and firstCommandComplete so both
// treat the leading run identically.
func stripLeadingSeparators(s string) string {
	s = strings.TrimLeft(s, " \t")
	for {
		t := strings.TrimLeft(strings.TrimPrefix(strings.TrimPrefix(s, "&&"), ";"), " \t")
		if t == s {
			return s
		}
		s = t
	}
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
}

// NewCodestral builds a Provider backed by a shared, keep-alive-tuned
// http.Client. Defaults: baseURL "https://api.mistral.ai", model
// "codestral-latest" — the design originally named codestral.mistral.ai, but
// a pay-as-you-go Mistral account cannot mint a Codestral-specific key, so
// the general per-token endpoint (which serves the same FIM endpoint under a
// regular Mistral key) is the default. baseURL stays configurable so
// codestral.mistral.ai still works for anyone holding that key.
func NewCodestral(baseURL, model, apiKey string, maxTokens int) (Provider, error) {
	if baseURL == "" {
		baseURL = "https://api.mistral.ai"
	}
	if model == "" {
		model = "codestral-latest"
	}
	transport := &http.Transport{
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        16,
		MaxIdleConnsPerHost: 16,
		IdleConnTimeout:     90 * time.Second,
	}
	return &codestralClient{
		baseURL:   strings.TrimRight(baseURL, "/"),
		model:     model,
		apiKey:    apiKey,
		maxTokens: maxTokens,
		stop:      fimStopSequences,
		http:      &http.Client{Transport: transport},
	}, nil
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

// RenderFIM renders a Prompt for a FIM endpoint. FIM models take no system
// role, so the context block is encoded as shell comment lines above the
// buffer. Suffix is "" today, making this pure prefix continuation — exactly
// what FIM models are trained to nail.
func RenderFIM(p prompt.Prompt) (fimPrompt, suffix string) {
	// p.Context is pre-rendered like "Context:\n- cwd: ...\n- git: ...\n\n"
	// (prompt.contextBlock). Strip the "Context:" header and the trailing
	// blank-line separator, then re-render each remaining "- " line as a "#"
	// shell comment. p.System and p.Instruction are chat-model append-contract
	// text a FIM model neither needs nor benefits from, so they are
	// deliberately excluded.
	ctx := strings.TrimPrefix(p.Context, "Context:\n")
	ctx = strings.TrimSuffix(ctx, "\n\n")
	if ctx == "" {
		return p.Prefix, p.Suffix
	}

	var b strings.Builder
	for _, line := range strings.Split(ctx, "\n") {
		b.WriteString("# ")
		b.WriteString(strings.TrimPrefix(line, "- "))
		b.WriteString("\n")
	}
	// Buffer goes last, with no trailing newline, so the model's completion
	// continues directly from it.
	b.WriteString(p.Prefix)
	return b.String(), p.Suffix
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
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = c.maxTokens
	}
	fimPrompt, suffix := RenderFIM(req.Prompt)
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
				Text:         firstShellCommand(acc.Text()),
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
		Text:         firstShellCommand(acc.Text()),
		TTFT:         acc.TTFT(),
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		CachedTokens: cachedTokens,
		HTTPStatus:   resp.StatusCode,
		StopReason:   stopReason,
	}, nil
}
