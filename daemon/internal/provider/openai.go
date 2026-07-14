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
}

// streamChunk mirrors one SSE "data:" event's JSON payload — just enough to
// pull out the incremental token(s) in choices[0].delta.content.
type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
}

// Complete issues a streaming chat-completions request and returns the
// model's first line of output (design §4 "stream + take first line only").
//
// It honors ctx throughout: the HTTP request itself is built with
// NewRequestWithContext, so cancelling ctx (e.g. because a newer keystroke
// superseded this request — see server.handle) aborts the call, including
// mid-stream reads of the response body.
func (c *Client) Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	reqBody := chatRequest{
		Model: c.model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		MaxTokens: c.maxTokens,
		Stream:    true,
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("provider: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("provider: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		// Covers network errors AND ctx cancellation/deadline (http.Client.Do
		// returns ctx.Err(), possibly wrapped, when ctx ends before or during
		// the round trip).
		return "", fmt.Errorf("provider: request: %w", err)
	}
	// Closing the body (a) prevents leaking the connection on every return
	// path below, including the early "first newline seen" cutoff, and (b)
	// is what actually aborts the in-progress read on a slow/blocked stream
	// once ctx is cancelled or we simply stop caring.
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("provider: unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	// SSE parsing: the response body is a sequence of lines. Each event we
	// care about looks like "data: {...json...}\n"; a blank line separates
	// events but bufio.Scanner's default ScanLines split already gives us
	// one line at a time, so we just filter for the "data:" prefix and skip
	// everything else (blank keep-alive lines, comments, other fields).
	var acc strings.Builder
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
			return "", fmt.Errorf("provider: malformed stream chunk: %w", err)
		}
		if len(chunk.Choices) == 0 {
			continue
		}

		acc.WriteString(chunk.Choices[0].Delta.Content)

		// First-line cutoff (design §4): as soon as the accumulated text
		// contains a newline, we have a complete single-line suggestion and
		// stop reading immediately — we don't need the rest of the
		// completion (often prose/explanation past the first line), and
		// returning now is the whole point of streaming at all.
		if text := acc.String(); strings.ContainsRune(text, '\n') {
			return text[:strings.IndexByte(text, '\n')], nil
		}
	}

	if serr := scanner.Err(); serr != nil {
		// Prefer ctx.Err() when set: a cancelled/expired ctx is what actually
		// aborted the read, and scanner.Err() would otherwise surface as an
		// opaque wrapped "context canceled" from the transport anyway.
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("provider: read stream: %w", serr)
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}

	// Stream ended (EOF / [DONE] / max_tokens finish) with no newline seen:
	// return whatever we accumulated as the whole (single-line) completion.
	return acc.String(), nil
}
