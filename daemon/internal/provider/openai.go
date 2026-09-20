// openai.go adapts the official openai-go v3 SDK to the Provider seam.
// Covers the whole OpenAI-compatible ecosystem (Groq, Together, Ollama, ...)
// by swapping base URL, model, and key; nothing is brand-specific.
package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"github.com/naasanov/zsh-autopilot/daemon/internal/prompt"
)

// Reasoning models special-cased in Complete: qwen3.6-27b can fully disable
// its <think> block; gpt-oss-20b cannot.
const (
	qwenReasoningModel = "qwen/qwen3.6-27b"
	gptOSS20BModel     = "openai/gpt-oss-20b"
)

// Construct via NewOpenAI and reuse for every request; it holds a shared
// *http.Client.
type openAIClient struct {
	client    openai.Client
	model     string
	maxTokens int
	prompt    prompt.ChatPrompt
}

// Call once at startup, not per-request: a fresh client per request
// defeats the warm-connection point.
func NewOpenAI(baseURL, model, apiKey string, maxTokens int, p prompt.ChatPrompt) (Provider, error) {
	client := openai.NewClient(
		option.WithBaseURL(baseURL),
		option.WithAPIKey(apiKey),
		option.WithHTTPClient(keepAliveHTTPClient()),
		option.WithMiddleware(rateLimitMiddleware),
		// Retries disabled: a single attempt's timing is what TTFT measures.
		option.WithMaxRetries(0),
	)

	return &openAIClient{
		client:    client,
		model:     model,
		maxTokens: maxTokens,
		prompt:    p,
	}, nil
}

func (c *openAIClient) Name() string {
	return "openai"
}

func (c *openAIClient) Model() string {
	return c.model
}

// RenderPrompt returns the exact system+user text Complete would send.
func (c *openAIClient) RenderPrompt(req Request) string {
	return RenderChatPrompt(c.prompt.RenderChat(req.Req))
}

func (c *openAIClient) PromptName() string {
	return c.prompt.Name()
}

type rateLimitCtxKey struct{}

// Reached via context.WithValue, not a client field, since one openAIClient
// is shared across concurrent Complete calls.
type rateLimitHolder struct {
	rl *RateLimit
}

// merge folds one response's observation into the accumulated RateLimit:
// token fields are replaced only when this response carried them; RetryAfter
// is kept as the max seen across every attempt. A nil rl is a no-op.
func (h *rateLimitHolder) merge(rl *RateLimit, hasTokens bool) {
	if rl == nil {
		return
	}
	if h.rl == nil {
		h.rl = &RateLimit{}
	}
	if hasTokens {
		h.rl.LimitTokens = rl.LimitTokens
		h.rl.RemainingTokens = rl.RemainingTokens
		h.rl.ResetTokens = rl.ResetTokens
	}
	if rl.RetryAfter > h.rl.RetryAfter {
		h.rl.RetryAfter = rl.RetryAfter
	}
}

// Checked directly against the response, not parseRateLimit's output, to
// distinguish absent headers from headers carrying zero.
var tokenHeaderKeys = []string{
	"x-ratelimit-limit-tokens",
	"x-ratelimit-remaining-tokens",
	"x-ratelimit-reset-tokens",
}

func hasTokenHeaders(h http.Header) bool {
	for _, k := range tokenHeaderKeys {
		if h.Get(k) != "" {
			return true
		}
	}
	return false
}

func rateLimitMiddleware(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
	resp, err := next(req)
	if resp != nil {
		if h, ok := req.Context().Value(rateLimitCtxKey{}).(*rateLimitHolder); ok {
			h.merge(parseRateLimit(resp.Header), hasTokenHeaders(resp.Header))
		}
	}
	return resp, err
}

// parseRateLimit reads Groq/OpenAI-style rate-limit headers, populating only
// fields whose header was present (a 429 sends retry-after with no
// x-ratelimit-* headers; that must not read as "0 tokens remaining").
func parseRateLimit(h http.Header) *RateLimit {
	var rl RateLimit
	var seen bool

	if v := h.Get("x-ratelimit-limit-tokens"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			rl.LimitTokens = n
			seen = true
		}
	}
	if v := h.Get("x-ratelimit-remaining-tokens"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			rl.RemainingTokens = n
			seen = true
		}
	}
	if v := h.Get("x-ratelimit-reset-tokens"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			rl.ResetTokens = d
			seen = true
		}
	}
	if v := h.Get("retry-after"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil {
			rl.RetryAfter = time.Duration(secs) * time.Second
			seen = true
		}
	}

	if !seen {
		return nil
	}
	return &rl
}

// Cancelling ctx aborts the call, including mid-stream reads.
func (c *openAIClient) Complete(ctx context.Context, req Request) (Completion, error) {
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = c.maxTokens
	}

	pl := c.prompt.RenderChat(req.Req)

	params := openai.ChatCompletionNewParams{
		Model: c.model,
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(pl.System),
			openai.UserMessage(pl.User),
		},
		MaxTokens: openai.Int(int64(maxTokens)),
		// Emits a final SSE chunk carrying a usage object.
		StreamOptions: openai.ChatCompletionStreamOptionsParam{
			IncludeUsage: openai.Bool(true),
		},
	}

	acc := newAccumulator(time.Now())

	holder := &rateLimitHolder{}
	ctx = context.WithValue(ctx, rateLimitCtxKey{}, holder)

	opts := []option.RequestOption{}
	switch c.model {
	case qwenReasoningModel:
		// Without this, qwen3.6-27b spends its whole token budget on a
		// hidden <think> block before any visible output.
		opts = append(opts, option.WithJSONSet("reasoning_effort", "none"))
	case gptOSS20BModel:
		// gpt-oss rejects reasoning_effort:"none" outright (400); "low" is
		// the quietest setting the API accepts, and still costs tokens.
		opts = append(opts, option.WithJSONSet("reasoning_effort", "low"))
	}

	stream := c.client.Chat.Completions.NewStreaming(ctx, params, opts...)
	// Releases the response body/connection on every return path, and
	// aborts an in-progress read on ctx cancellation.
	defer stream.Close()

	var stopReason string
	var inputTokens, outputTokens, cachedTokens int
	for stream.Next() {
		chunk := stream.Current()

		// Usage lands on its own final chunk (choices may be empty there);
		// checked via Valid() since the zero value is otherwise
		// indistinguishable from "absent". Must precede the len==0 skip below.
		if chunk.JSON.Usage.Valid() {
			inputTokens = int(chunk.Usage.PromptTokens)
			outputTokens = int(chunk.Usage.CompletionTokens)
			cachedTokens = int(chunk.Usage.PromptTokensDetails.CachedTokens)
		}
		if len(chunk.Choices) == 0 {
			continue
		}

		if chunk.Choices[0].FinishReason != "" {
			stopReason = chunk.Choices[0].FinishReason
		}

		// Token counts are typically zero here: the usage chunk hasn't arrived yet.
		if stop := acc.Push(chunk.Choices[0].Delta.Content); stop {
			return Completion{
				Text:         acc.Text(),
				TTFT:         acc.TTFT(),
				InputTokens:  inputTokens,
				OutputTokens: outputTokens,
				CachedTokens: cachedTokens,
				HTTPStatus:   http.StatusOK,
				StopReason:   stopReason,
				RateLimit:    holder.rl,
			}, nil
		}
	}

	if err := stream.Err(); err != nil {
		// ctx.Err() is preferred: the raw error may surface as an opaque
		// wrapped "context canceled".
		if ctx.Err() != nil {
			return Completion{}, &Error{Kind: ErrCanceled, Provider: c.Name(), Err: ctx.Err()}
		}

		// *openai.Error is what the SDK returns for any non-2xx response; it
		// carries the HTTP status code.
		var apiErr *openai.Error
		if errors.As(err, &apiErr) {
			return Completion{HTTPStatus: apiErr.StatusCode, RateLimit: holder.rl}, &Error{
				Kind:       ClassifyHTTP(apiErr.StatusCode),
				HTTPStatus: apiErr.StatusCode,
				Provider:   c.Name(),
				Err:        fmt.Errorf("provider: unexpected status %d: %s", apiErr.StatusCode, apiErr.Message),
			}
		}

		// Anything else is a pre-response transport failure (DNS, connection
		// refused, TLS, stream read error, ...).
		return Completion{}, &Error{Kind: ErrTransport, Provider: c.Name(), Err: fmt.Errorf("provider: request: %w", err)}
	}

	// No newline seen before the stream ended: return everything accumulated.
	return Completion{
		Text:         acc.Text(),
		TTFT:         acc.TTFT(),
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		CachedTokens: cachedTokens,
		HTTPStatus:   http.StatusOK,
		StopReason:   stopReason,
		RateLimit:    holder.rl,
	}, nil
}
