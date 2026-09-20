// codestral.go is a hand-rolled client for Mistral's Codestral FIM
// (fill-in-the-middle) endpoint: prompt+suffix, not chat messages, hence
// its own adapter. No SDK: Mistral ships none.
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

var fimStopSequences = []string{"\n"}

// Cuts at the first top-level ";" or "&&", but not "|", which is within
// one command, not a chain.
func firstShellCommand(s string, typing bool) string {
	s = stripLeadingSeparators(s, typing)
	if i := indexSeparator(s); i >= 0 {
		s = s[:i]
	}
	return strings.TrimRight(s, " \t")
}

// Leading separators are stripped first, so a leading "; " isn't
// mistaken as done.
func firstCommandComplete(s string) bool {
	return indexSeparator(stripLeadingSeparators(s, false)) >= 0
}

// Typing mode preserves exactly one leading space when no separator is
// found: dropping it collapses "git add" + "." into "git add.".
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

	if foundSeparator || !typing {
		return strings.TrimLeft(cur, " \t")
	}

	trimmed := strings.TrimLeft(cur, " \t")
	if cur == trimmed {
		return cur
	}
	if trimmed == "" {
		return cur
	}
	return " " + trimmed
}

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

// Construct via NewCodestral and reuse: holds a shared, keep-alive
// http.Client.
type codestralClient struct {
	baseURL   string
	model     string
	apiKey    string
	maxTokens int
	stop      []string
	http      *http.Client
	prompt    prompt.FIMPrompt
}

func NewCodestral(baseURL, model, apiKey string, maxTokens int, p prompt.FIMPrompt) (Provider, error) {
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
		prompt:    p,
	}
	return c, nil
}

func (c *codestralClient) Name() string {
	return "codestral"
}

func (c *codestralClient) Model() string {
	return c.model
}

// RenderPrompt returns the exact FIM prompt(+suffix) Complete would send.
func (c *codestralClient) RenderPrompt(req Request) string {
	pl := c.prompt.RenderFIM(req.Req)
	if pl.Suffix == "" {
		return pl.Prefix
	}
	return "PROMPT:\n" + pl.Prefix + "\n\nSUFFIX:\n" + pl.Suffix
}

func (c *codestralClient) PromptName() string {
	return c.prompt.Name()
}

type fimRequest struct {
	Model     string   `json:"model"`
	Prompt    string   `json:"prompt"`
	Suffix    string   `json:"suffix"`
	MaxTokens int      `json:"max_tokens"`
	Stream    bool     `json:"stream"`
	Stop      []string `json:"stop,omitempty"`
}

// fimChunk mirrors one SSE "data:" event; Codestral's FIM stream is
// OpenAI-shaped (choices[0].delta.content).
type fimChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		// finish_reason ("stop", "length", ...) is set on the last per-choice chunk.
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	// Usage arrives on its own final chunk (choices may be empty there);
	// absent/zero is expected whenever the cutoff returns before it arrives.
	Usage *fimUsage `json:"usage"`
}

// fimUsage mirrors the OpenAI-compatible usage object.
type fimUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	PromptTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

// Honors ctx throughout, including mid-stream reads.
func (c *codestralClient) Complete(ctx context.Context, req Request) (Completion, error) {
	typing := req.Req.Buf != ""

	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = c.maxTokens
	}
	pl := c.prompt.RenderFIM(req.Req)
	reqBody := fimRequest{
		Model:     c.model,
		Prompt:    pl.Prefix,
		Suffix:    pl.Suffix,
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

	acc := newAccumulator(time.Now())

	resp, err := c.http.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return Completion{}, &Error{Kind: ErrCanceled, Provider: c.Name(), Err: err}
		}
		return Completion{}, &Error{Kind: ErrTransport, Provider: c.Name(), Err: fmt.Errorf("provider: request: %w", err)}
	}
	// Also aborts the in-progress read on a cancelled ctx, not just cleanup.
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

		// Usage lands on its own final chunk (choices may be empty there),
		// so this check must happen before the len==0 skip below.
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

		// Token counts are typically zero here: the usage chunk hasn't arrived.
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
		// ctx.Err() is preferred: scanner.Err() surfaces cancellation as
		// an opaque wrapped "context canceled".
		if ctx.Err() != nil {
			return Completion{HTTPStatus: resp.StatusCode}, &Error{Kind: ErrCanceled, HTTPStatus: resp.StatusCode, Provider: c.Name(), Err: ctx.Err()}
		}
		return Completion{HTTPStatus: resp.StatusCode}, &Error{Kind: ErrTransport, HTTPStatus: resp.StatusCode, Provider: c.Name(), Err: fmt.Errorf("provider: read stream: %w", serr)}
	}
	if ctx.Err() != nil {
		return Completion{HTTPStatus: resp.StatusCode}, &Error{Kind: ErrCanceled, HTTPStatus: resp.StatusCode, Provider: c.Name(), Err: ctx.Err()}
	}

	// No newline seen before the stream ended: return everything accumulated.
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
