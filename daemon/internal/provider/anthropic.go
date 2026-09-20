// anthropic.go is a native adapter for Anthropic's Messages API, built on
// anthropic-sdk-go.
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

const defaultAnthropicModel = "claude-haiku-4-5"

// Construct via NewAnthropic and reuse: the underlying anthropic.Client
// holds its own warm http.Client.
type anthropicClient struct {
	client    anthropic.Client
	model     string
	maxTokens int
	prompt    prompt.ChatPrompt
}

// There is no baseURL parameter: the native endpoint is fixed.
func NewAnthropic(model, apiKey string, maxTokens int, p prompt.ChatPrompt) (Provider, error) {
	return newAnthropicClient(model, apiKey, maxTokens, "", p)
}

// baseURL lets tests aim this at an httptest.Server; production goes
// through NewAnthropic.
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

func (c *anthropicClient) Name() string {
	return "anthropic"
}

func (c *anthropicClient) Model() string {
	return c.model
}

// RenderPrompt returns the exact system+user text Complete would send.
func (c *anthropicClient) RenderPrompt(req Request) string {
	return RenderChatPrompt(c.prompt.RenderChat(req.Req))
}

func (c *anthropicClient) PromptName() string {
	return c.prompt.Name()
}

// Cancelling ctx aborts the call, including mid-stream reads.
func (c *anthropicClient) Complete(ctx context.Context, req Request) (Completion, error) {
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = c.maxTokens
	}

	pl := c.prompt.RenderChat(req.Req)

	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(c.model),
		MaxTokens: int64(maxTokens),
		// No CacheControl on the system block: under Haiku 4.5's
		// 4096-token minimum cacheable prefix.
		System: []anthropic.TextBlockParam{{Text: pl.System}},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(pl.User)),
		},
	}

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
			// Stops without draining the trailing usage event; deferred
			// stream.Close() tears down the in-flight read.
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
			// Usage on message_delta is cumulative so far; stop_reason
			// lands here too.
			if eventVariant.Delta.StopReason != "" {
				stopReason = string(eventVariant.Delta.StopReason)
			}
			inputTokens = int(eventVariant.Usage.InputTokens)
			outputTokens = int(eventVariant.Usage.OutputTokens)
			cachedTokens = int(eventVariant.Usage.CacheReadInputTokens)
		}
	}

	if err := stream.Err(); err != nil {
		// ctx.Err() is preferred: the raw SDK error surfaces cancellation
		// as an opaque wrapped "context canceled".
		if ctx.Err() != nil {
			return Completion{}, &Error{Kind: ErrCanceled, Provider: c.Name(), Err: err}
		}
		// anthropic.Error is the SDK's typed API error and carries the HTTP status code.
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

	// No newline seen before the stream ended: return everything accumulated.
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
