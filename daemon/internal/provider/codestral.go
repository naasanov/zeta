// codestral.go is a hand-rolled client for Mistral's Codestral FIM
// (fill-in-the-middle) endpoint: a base completion model taking a
// prompt+suffix request shape instead of messages, hence its own adapter
// rather than another OpenAI-compatible base URL. No SDK: Mistral ships none.
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

// fimStopSequences halt generation early. Only the newline is used.
// Command-chaining (";"/"&&") is deliberately NOT a stop sequence: a next-
// command prediction often *leads* with a separator ("; source
// .venv/bin/activate"), and a ";" stop would halt on that leading char and
// return empty. Chains are handled by post-processing instead (see
// firstShellCommand), which a leading separator can't defeat.
var fimStopSequences = []string{"\n"}

// firstShellCommand extracts the first command from a raw FIM completion,
// stripping any leading separator/whitespace and cutting at the first
// top-level ";" or "&&" (left alone: "|", which is within one command, not a
// chain) — left unhandled, a code model chains commands on one line
// ("mkdir x; cd y; git init") or leads a next-command prediction with a
// stray separator.
//
// typing picks the whitespace rule in stripLeadingSeparators: true for a
// non-empty buffer, where the model's leading word-separator space must
// survive; false for next-command mode, where a leading space would
// silently keep the command out of zsh history under HIST_IGNORE_SPACE.
func firstShellCommand(s string, typing bool) string {
	s = stripLeadingSeparators(s, typing)
	if i := indexSeparator(s); i >= 0 {
		s = s[:i]
	}
	return strings.TrimRight(s, " \t")
}

// firstCommandComplete reports whether s already holds a complete first
// command followed by a separator, so the stream can stop early — everything
// past that separator would be discarded by firstShellCommand anyway.
// Leading separators are stripped first so a next-command prediction opening
// with "; " isn't mistaken for complete before its real command streams in
// (the empty-output bug a raw ";" stop sequence caused). The mode passed to
// stripLeadingSeparators only affects a single trailing leading space, never
// whether a separator is found, so it's arbitrary here.
func firstCommandComplete(s string) bool {
	return indexSeparator(stripLeadingSeparators(s, false)) >= 0
}

// stripLeadingSeparators removes leading ";"/"&&" separators and any
// whitespace touching them. If no separator is found, the remaining leading
// whitespace is the system prompt's word-separator space instead (see
// prompt.go), so it's mode-dependent: typing==true collapses it to exactly
// one space (must survive or "git add"+"." collides into "git add.");
// typing==false (next-command mode) strips it entirely, since a leading
// space would silently keep the command out of zsh history under
// HIST_IGNORE_SPACE.
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
// endpoint. Holds a shared *http.Client so the daemon isn't paying TLS/TCP
// setup per keystroke — construct one via NewCodestral and reuse it.
type codestralClient struct {
	baseURL   string
	model     string
	apiKey    string
	maxTokens int
	stop      []string
	http      *http.Client
	prompt    prompt.FIMPrompt
}

// NewCodestral builds a Provider backed by a shared, keep-alive-tuned
// http.Client. Defaults: baseURL "https://api.mistral.ai", model
// "codestral-latest" — a pay-as-you-go account can't mint a
// codestral.mistral.ai key, so the general per-token endpoint is the
// default; baseURL stays configurable for anyone holding that key.
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
// Suffix is "" in Phase 2 (protocol.Request carries no cursor position), so
// this is ordinarily just the FIM prompt; the SUFFIX: section only appears
// once the FIM infill hook is actually used.
func (c *codestralClient) RenderPrompt(req Request) string {
	pl := c.prompt.RenderFIM(req.Req)
	if pl.Suffix == "" {
		return pl.Prefix
	}
	return "PROMPT:\n" + pl.Prefix + "\n\nSUFFIX:\n" + pl.Suffix
}

// PromptName identifies the prompt this client renders with, for
// METRICS(§12) and eval reporting.
func (c *codestralClient) PromptName() string {
	return c.prompt.Name()
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
// model's first line of output, driving the shared accumulator for TTFT
// stamping and the cutoff. Honors ctx throughout, including mid-stream
// reads, via NewRequestWithContext.
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

	// METRICS(§12): TTFT from just before the round trip to the first chunk
	// with non-empty delta content.
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

		// Two early-stop conditions: a newline (shared accumulator), or a
		// shell separator (firstCommandComplete) — the rest of a chained
		// command would be discarded by firstShellCommand anyway, so stop
		// streaming it. Returning here means the trailing usage chunk hasn't
		// arrived, so token counts are typically zero on this path.
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
		// Prefer ctx.Err(): scanner.Err() would otherwise surface as an
		// opaque wrapped "context canceled" from the transport.
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
