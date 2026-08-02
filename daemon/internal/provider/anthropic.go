// anthropic.go is a native adapter for Anthropic's Messages API (design §6),
// built on anthropic-sdk-go. It is the "quality" provider profile, driving
// the SDK's own streaming client and error types, but still renders
// prompt.Prompt and drives the shared accumulator like every other Provider.
package provider

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/naasanov/zsh-autopilot/daemon/internal/prompt"
)

// defaultAnthropicModel is used when NewAnthropic is called with model == "".
const defaultAnthropicModel = "claude-haiku-4-5"

// anthropicClient talks to Anthropic's native Messages API. Construct one via
// NewAnthropic at daemon startup and reuse it for every request — the
// underlying anthropic.Client holds its own warm http.Client internally.
type anthropicClient struct {
	client    anthropic.Client
	model     string
	maxTokens int
	prompt    prompt.ChatPrompt
}

// NewAnthropic builds a Provider backed by anthropic-sdk-go's native client.
// There is no baseURL parameter — the native endpoint is fixed.
func NewAnthropic(model, apiKey string, maxTokens int, p prompt.ChatPrompt) (Provider, error) {
	return newAnthropicClient(model, apiKey, maxTokens, "", p)
}

// newAnthropicClient is the real constructor; the optional baseURL param
// exists only so anthropic_test.go can aim the client at an httptest.Server.
// Production always goes through NewAnthropic with baseURL == "".
func newAnthropicClient(model, apiKey string, maxTokens int, baseURL string, p prompt.ChatPrompt) (Provider, error) {
	if model == "" {
		model = defaultAnthropicModel
	}
	opts := []option.RequestOption{option.WithAPIKey(apiKey)}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	return &anthropicClient{
		client:    anthropic.NewClient(opts...),
		model:     model,
		maxTokens: maxTokens,
		prompt:    p,
	}, nil
}

// Name identifies this adapter for METRICS(§12) and price-table lookups.
func (c *anthropicClient) Name() string {
	return "anthropic"
}

// Model returns the model name this client was constructed with.
//
// METRICS(§12): used by internal/suggest to look up pricing for the
// "request" event's cost_usd field.
func (c *anthropicClient) Model() string {
	return c.model
}

// RenderPrompt returns the exact system+user text Complete would send,
// formatted via the shared chat-prompt helper (this adapter sends
// System + User as two messages, same as openai.go).
func (c *anthropicClient) RenderPrompt(req Request) string {
	return RenderChatPrompt(c.prompt.RenderChat(req.Req))
}

// PromptName identifies the prompt this client renders with, for
// METRICS(§12) and eval reporting.
func (c *anthropicClient) PromptName() string {
	return c.prompt.Name()
}

// Complete issues a streaming Messages API request and returns the model's
// first line of output (design §4), driving the shared accumulator for TTFT
// stamping and the cutoff. ctx is passed to NewStreaming, so cancelling it
// (e.g. a superseding keystroke) aborts the call, including mid-stream reads.
func (c *anthropicClient) Complete(ctx context.Context, req Request) (Completion, error) {
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = c.maxTokens
	}

	pl := c.prompt.RenderChat(req.Req)

	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(c.model),
		MaxTokens: int64(maxTokens),
		// No CacheControl breakpoint on the system block: the system prompt
		// is under Haiku 4.5's 4096-token minimum cacheable prefix, so
		// cache_control here would cache nothing — a silent no-op.
		// CachedTokens is still read below to catch it if that changes.
		System: []anthropic.TextBlockParam{{Text: pl.System}},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(pl.User)),
		},
		// Deliberately NOT set: Thinking (wrong for a sub-second completion),
		// OutputConfig.Effort (errors outright on Haiku 4.5), Temperature/TopP
		// (the system prompt already constrains output shape).
	}

	// METRICS(§12): TTFT is measured from just before the round trip starts to
	// the first chunk carrying non-empty text (accumulator.Push).
	acc := newAccumulator(time.Now())

	stream := c.client.Messages.NewStreaming(ctx, params)
	defer stream.Close()

	var stopReason string
	var inputTokens, outputTokens, cachedTokens int

	for stream.Next() {
		event := stream.Current()
		switch eventVariant := event.AsAny().(type) {
		case anthropic.ContentBlockDeltaEvent:
			deltaVariant, ok := eventVariant.Delta.AsAny().(anthropic.TextDelta)
			if !ok {
				continue
			}
			// First-line cutoff (design §4): stop and return the instant a
			// complete line is accumulated, without draining the stream for
			// the trailing message_delta usage event. defer stream.Close()
			// tears down the in-flight SSE read on this early-return path.
			if stop := acc.Push(deltaVariant.Text); stop {
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
		case anthropic.MessageDeltaEvent:
			// METRICS(§12): usage on message_delta is cumulative so far;
			// stop_reason lands here too. Only reached if the stream ends
			// without a newline (the cutoff above returns first otherwise).
			if eventVariant.Delta.StopReason != "" {
				stopReason = string(eventVariant.Delta.StopReason)
			}
			inputTokens = int(eventVariant.Usage.InputTokens)
			outputTokens = int(eventVariant.Usage.OutputTokens)
			cachedTokens = int(eventVariant.Usage.CacheReadInputTokens)
		}
	}

	if err := stream.Err(); err != nil {
		// Prefer ctx.Err() when set: a cancelled/expired ctx is what actually
		// aborted the stream, and the raw SDK error otherwise surfaces as an
		// opaque wrapped "context canceled" from the transport anyway (same
		// preference order as openai.go).
		if ctx.Err() != nil {
			return Completion{}, &Error{Kind: ErrCanceled, Provider: c.Name(), Err: err}
		}
		// anthropic.Error is the SDK's typed API error (a type alias for
		// internal/apierror.Error) and carries the HTTP StatusCode. Extract it
		// via errors.As so ClassifyHTTP gets a real status code whenever the
		// failure came from the API rather than the network.
		var apiErr *anthropic.Error
		if errors.As(err, &apiErr) {
			return Completion{HTTPStatus: apiErr.StatusCode}, &Error{
				Kind:       ClassifyHTTP(apiErr.StatusCode),
				HTTPStatus: apiErr.StatusCode,
				Provider:   c.Name(),
				Err:        err,
			}
		}
		return Completion{}, &Error{Kind: ErrTransport, Provider: c.Name(), Err: err}
	}

	// Stream ended (message_stop / EOF) with no newline ever seen: return
	// whatever we accumulated as the whole (single-line) completion.
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
