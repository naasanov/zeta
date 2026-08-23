// openai.go adapts the official openai-go v3 SDK to the Provider seam
// (design §6). Covers the whole OpenAI-compatible ecosystem (Groq, OpenAI,
// Together, Ollama, ...) by swapping base URL + model + key — nothing is
// hardcoded to a specific brand.
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

// Reasoning models this adapter special-cases in Complete: qwen3.6-27b can
// fully disable its <think> block via reasoning_effort:"none"; gpt-oss-20b
// (the shipped groq preset) cannot — "low" is its quietest setting and still
// costs tokens, hence the MaxTokens override on the groq preset.
const (
	qwenReasoningModel = "qwen/qwen3.6-27b"
	gptOSS20BModel     = "openai/gpt-oss-20b"
)

// openAIClient talks to a single OpenAI-compatible /chat/completions
// endpoint via the openai-go SDK client, holding a shared *http.Client
// (design §4 warm connections) — construct via NewOpenAI at startup and
// reuse for every request.
type openAIClient struct {
	client    openai.Client
	model     string
	maxTokens int
	prompt    prompt.ChatPrompt
}

// NewOpenAI builds a Provider backed by the openai-go SDK client, pointed at
// baseURL with apiKey. Call once at daemon startup, not per-request — a fresh
// client per request defeats the warm-connection point.
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

// Name identifies this adapter for METRICS(§12) and price-table lookups.
func (c *openAIClient) Name() string {
	return "openai"
}

// Model returns the model name this client was constructed with.
//
// METRICS(§12): used by internal/suggest to look up pricing for the
// "request" event's cost_usd field.
func (c *openAIClient) Model() string {
	return c.model
}

// RenderPrompt returns the exact system+user text Complete would send,
// formatted via the shared chat-prompt helper (this adapter sends
// System + User as two messages, same as anthropic.go).
func (c *openAIClient) RenderPrompt(req Request) string {
	return RenderChatPrompt(c.prompt.RenderChat(req.Req))
}

// PromptName identifies the prompt this client renders with, for
// METRICS(§12) and eval reporting.
func (c *openAIClient) PromptName() string {
	return c.prompt.Name()
}

// rateLimitCtxKey is the unexported key Complete uses to stash a
// rateLimitHolder on a per-call context; distinct types keep it collision-free
// with any other package's context values.
type rateLimitCtxKey struct{}

// rateLimitHolder carries one Complete call's observed RateLimit across HTTP
// attempts (retries are disabled by NewOpenAI, but a redirect or a future
// re-enable could still produce more than one). It is reached via
// context.WithValue rather than a client field because one openAIClient is
// shared across concurrent Complete calls.
type rateLimitHolder struct {
	rl *RateLimit
}

// merge folds one response's observation into the accumulated RateLimit:
// token fields are replaced only when this response actually carried them,
// RetryAfter is kept as the max seen across every attempt. A nil rl is a
// no-op, safe on a holder with no prior observation either.
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

// tokenHeaderKeys are checked directly against the response (not through
// parseRateLimit's output) so merge can tell "this response carried token
// headers" apart from "it carried them with value zero".
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

// rateLimitMiddleware merges each HTTP response's rate-limit headers into
// the holder Complete placed on the request context (a no-op if absent).
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

// Complete issues a streaming chat-completions request via the SDK and
// returns the model's first line of output (design §4), driving the shared
// accumulator for TTFT stamping and the cutoff. ctx is passed straight into
// NewStreaming, so cancelling it (e.g. a superseding keystroke) aborts the
// call, including mid-stream reads.
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
		// METRICS(§12): ask the OpenAI-compatible endpoint to emit a final
		// SSE chunk carrying a usage object, decoded below via chunk.Usage.
		StreamOptions: openai.ChatCompletionStreamOptionsParam{
			IncludeUsage: openai.Bool(true),
		},
	}

	// METRICS(§12): TTFT is measured from just before the round trip starts
	// to the first chunk carrying non-empty delta content (accumulator.Push).
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
		// the quietest setting the API accepts, and still costs tokens —
		// covered by the groq preset's MaxTokens override.
		opts = append(opts, option.WithJSONSet("reasoning_effort", "low"))
	}

	stream := c.client.Chat.Completions.NewStreaming(ctx, params, opts...)
	// Closing the stream (a) releases the underlying response body/connection
	// on every return path below, including the early "first newline seen"
	// cutoff, and (b) is what actually aborts the in-progress read on a
	// slow/blocked stream once ctx is cancelled or we simply stop caring.
	defer stream.Close()

	var stopReason string
	var inputTokens, outputTokens, cachedTokens int
	for stream.Next() {
		chunk := stream.Current()

		// METRICS(§12): usage lands on its own final chunk (choices may be
		// empty there); checked via chunk.JSON.Usage.Valid() since
		// CompletionUsage's zero value is otherwise indistinguishable from
		// "absent". Must happen before the len==0 skip below.
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

		// First-line cutoff (design §4): stop reading the instant the
		// accumulated text has a newline. METRICS(§12): this returns before
		// the trailing usage chunk arrives, so token counts are typically
		// zero here — expected, and not a reason to remove the cutoff.
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
		// ctx cancellation surfaces as either the bare ctx.Err() (initial
		// round trip) or an opaque wrapped "context canceled" (mid-stream
		// read abort); prefer ctx.Err() itself so errors.Is(err,
		// context.Canceled) is guaranteed either way.
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

	// Stream ended (EOF / [DONE] / max_tokens finish) with no newline seen:
	// return whatever we accumulated as the whole (single-line) completion.
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
