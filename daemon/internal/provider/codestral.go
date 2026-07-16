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

// codestralClient talks to a single Mistral-compatible /v1/fim/completions
// endpoint. It holds a shared *http.Client so the daemon never pays TLS/TCP
// setup cost per keystroke (design §4 "warm connections") — construct one via
// NewCodestral at startup and reuse it for every request.
type codestralClient struct {
	baseURL   string
	model     string
	apiKey    string
	maxTokens int
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
	Model     string `json:"model"`
	Prompt    string `json:"prompt"`
	Suffix    string `json:"suffix"`
	MaxTokens int    `json:"max_tokens"`
	Stream    bool   `json:"stream"`
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

		// First-line cutoff (design §4): as soon as the accumulated text
		// contains a newline, we have a complete single-line suggestion and
		// stop reading immediately — we don't need the rest of the
		// completion (often prose/explanation past the first line), and
		// returning now is the whole point of streaming at all.
		//
		// METRICS(§12) note: this returns before the trailing usage chunk
		// arrives, so InputTokens/OutputTokens/CachedTokens will typically be
		// zero on this path. That's expected and acceptable — the cutoff is
		// the whole point of streaming and must not be removed to chase
		// usage stats.
		if stop := acc.Push(chunk.Choices[0].Delta.Content); stop {
			return Completion{
				Text:         acc.Text(),
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
		Text:         acc.Text(),
		TTFT:         acc.TTFT(),
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		CachedTokens: cachedTokens,
		HTTPStatus:   resp.StatusCode,
		StopReason:   stopReason,
	}, nil
}
