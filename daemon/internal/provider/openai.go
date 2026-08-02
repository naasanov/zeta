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
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"github.com/naasanov/zsh-autopilot/daemon/internal/prompt"
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

	stream := c.client.Chat.Completions.NewStreaming(ctx, params)
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
			return Completion{HTTPStatus: apiErr.StatusCode}, &Error{
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
	}, nil
}
