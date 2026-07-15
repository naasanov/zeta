// Package provider is a small, hand-rolled client for OpenAI-compatible
// streaming chat-completions endpoints (design §6). Phase 1 targets Groq by
// default, but any OpenAI-shaped /chat/completions endpoint (Groq, OpenAI,
// Together, Ollama, ...) works by swapping base URL + model + key. This is
// deliberately NOT a formal Provider interface with multiple adapters — that
// generalization (native Anthropic adapter, openai-go SDK, TOML profiles) is
// Phase 2. Today there is exactly one concrete Client and one caller.
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
)

// Client talks to a single OpenAI-compatible /chat/completions endpoint. It
// holds a shared *http.Client so the daemon never pays TLS/TCP setup cost per
// keystroke (design §4 "warm connections") — construct one Client at startup
// and reuse it for every request.
type Client struct {
	baseURL   string
	model     string
	apiKey    string
	maxTokens int
	http      *http.Client
}

// NewClient builds a Client with a shared, keep-alive-tuned http.Client. Call
// this once at daemon startup, not per-request: a fresh http.Client (and
// therefore a fresh connection pool) defeats the whole warm-connection point.
func NewClient(baseURL, model, apiKey string, maxTokens int) *Client {
	transport := &http.Transport{
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        16,
		MaxIdleConnsPerHost: 16,
		IdleConnTimeout:     90 * time.Second,
	}
	return &Client{
		baseURL:   strings.TrimRight(baseURL, "/"),
		model:     model,
		apiKey:    apiKey,
		maxTokens: maxTokens,
		http:      &http.Client{Transport: transport},
	}
}

// Model returns the model name this Client was constructed with.
//
// METRICS(§12): used by internal/suggest to look up pricing for the
// "request" event's cost_usd field.
func (c *Client) Model() string {
	return c.model
}

// chatMessage and chatRequest mirror just the subset of the OpenAI
// chat-completions request schema this client needs.
type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model     string        `json:"model"`
	Messages  []chatMessage `json:"messages"`
	MaxTokens int           `json:"max_tokens"`
	Stream    bool          `json:"stream"`
	// METRICS(§12): ask the OpenAI-compatible endpoint to emit a final SSE
	// chunk carrying a usage object (prompt/completion token counts), which
	// streamChunk.Usage below decodes. Harmless to leave wired even if
	// metrics are stripped later — it's a cheap byproduct, not a metrics
	// dependency.
	StreamOptions *streamOptions `json:"stream_options,omitempty"`
}

// METRICS(§12): streamOptions requests the trailing usage chunk.
type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// streamChunk mirrors one SSE "data:" event's JSON payload — just enough to
// pull out the incremental token(s) in choices[0].delta.content, plus
// (METRICS §12) choices[0].finish_reason and a top-level usage object that
// arrives on the final chunk when stream_options.include_usage is set.
type streamChunk struct {
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
	Usage *streamUsage `json:"usage"`
}

// METRICS(§12): streamUsage mirrors the OpenAI-compatible usage object.
// Cached input tokens live under prompt_tokens_details.cached_tokens in the
// OpenAI-compatible shape; decoded defensively since providers vary in
// whether they populate it.
type streamUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	PromptTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

// Completion is the result of a successful (or partially successful, on the
// HTTP-error path) Complete call: the suggestion text plus METRICS(§12)
// provider-internal stats used to build the "request" event. HTTPStatus is
// populated even on error returns so the caller can log the status of a
// failed call.
type Completion struct {
	Text         string
	TTFT         time.Duration
	InputTokens  int
	OutputTokens int
	CachedTokens int
	HTTPStatus   int
	StopReason   string
}

// Complete issues a streaming chat-completions request and returns the
// model's first line of output (design §4 "stream + take first line only").
//
// It honors ctx throughout: the HTTP request itself is built with
// NewRequestWithContext, so cancelling ctx (e.g. because a newer keystroke
// superseded this request — see server.handle) aborts the call, including
// mid-stream reads of the response body.
func (c *Client) Complete(ctx context.Context, systemPrompt, userPrompt string) (Completion, error) {
	reqBody := chatRequest{
		Model: c.model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		MaxTokens: c.maxTokens,
		Stream:    true,
		// METRICS(§12): request the trailing usage chunk.
		StreamOptions: &streamOptions{IncludeUsage: true},
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return Completion{}, fmt.Errorf("provider: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return Completion{}, fmt.Errorf("provider: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Accept", "text/event-stream")

	// METRICS(§12): TTFT is measured from just before the round trip starts
	// to the first chunk carrying non-empty delta content (below).
	sendStart := time.Now()

	resp, err := c.http.Do(httpReq)
	if err != nil {
		// Covers network errors AND ctx cancellation/deadline (http.Client.Do
		// returns ctx.Err(), possibly wrapped, when ctx ends before or during
		// the round trip).
		return Completion{}, fmt.Errorf("provider: request: %w", err)
	}
	// Closing the body (a) prevents leaking the connection on every return
	// path below, including the early "first newline seen" cutoff, and (b)
	// is what actually aborts the in-progress read on a slow/blocked stream
	// once ctx is cancelled or we simply stop caring.
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return Completion{HTTPStatus: resp.StatusCode}, fmt.Errorf("provider: unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	// SSE parsing: the response body is a sequence of lines. Each event we
	// care about looks like "data: {...json...}\n"; a blank line separates
	// events but bufio.Scanner's default ScanLines split already gives us
	// one line at a time, so we just filter for the "data:" prefix and skip
	// everything else (blank keep-alive lines, comments, other fields).
	var acc strings.Builder
	var ttft time.Duration
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

		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return Completion{HTTPStatus: resp.StatusCode}, fmt.Errorf("provider: malformed stream chunk: %w", err)
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

		delta := chunk.Choices[0].Delta.Content
		if delta != "" && ttft == 0 {
			// METRICS(§12): TTFT = time from just before c.http.Do to the
			// first chunk with non-empty delta content.
			ttft = time.Since(sendStart)
		}
		acc.WriteString(delta)

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
		if text := acc.String(); strings.ContainsRune(text, '\n') {
			return Completion{
				Text:         text[:strings.IndexByte(text, '\n')],
				TTFT:         ttft,
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
			return Completion{HTTPStatus: resp.StatusCode}, ctx.Err()
		}
		return Completion{HTTPStatus: resp.StatusCode}, fmt.Errorf("provider: read stream: %w", serr)
	}
	if ctx.Err() != nil {
		return Completion{HTTPStatus: resp.StatusCode}, ctx.Err()
	}

	// Stream ended (EOF / [DONE] / max_tokens finish) with no newline seen:
	// return whatever we accumulated as the whole (single-line) completion.
	return Completion{
		Text:         acc.String(),
		TTFT:         ttft,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		CachedTokens: cachedTokens,
		HTTPStatus:   resp.StatusCode,
		StopReason:   stopReason,
	}, nil
}
