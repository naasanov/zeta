// anthropic.go is a native adapter for Anthropic's Messages API (design §6),
// built on anthropic-sdk-go. It is the "quality" provider profile: unlike
// openai.go's hand-rolled OpenAI-compatible client, this adapter drives the
// SDK's own streaming client and error types, but still renders prompt.Prompt
// and drives the shared accumulator exactly like every other Provider.
package provider

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// defaultAnthropicModel is used when NewAnthropic is called with model == "".
const defaultAnthropicModel = "claude-haiku-4-5"

// anthropicClient talks to Anthropic's native Messages API. Construct one via
// NewAnthropic at daemon startup and reuse it for every request — the
// underlying anthropic.Client holds its own warm http.Client internally, so a
// fresh anthropicClient per request would defeat the same warm-connection
// point openai.go cares about (design §4).
type anthropicClient struct {
	client    anthropic.Client
	model     string
	maxTokens int
}

// NewAnthropic builds a Provider backed by anthropic-sdk-go's native client.
// There is no baseURL parameter — the native endpoint is fixed — but see
// newAnthropicClient (test-only) for how the test suite points this at an
// httptest server.
func NewAnthropic(model, apiKey string, maxTokens int) (Provider, error) {
	return newAnthropicClient(model, apiKey, maxTokens, "")
}

// newAnthropicClient is the real constructor; NewAnthropic is a thin public
// wrapper that never sets baseURL (the fixed native endpoint). The optional
// baseURL parameter exists ONLY so anthropic_test.go can aim the client at an
// httptest.Server via option.WithBaseURL — production code always goes
// through NewAnthropic with baseURL == "".
func newAnthropicClient(model, apiKey string, maxTokens int, baseURL string) (Provider, error) {
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

// Complete issues a streaming Messages API request and returns the model's
// first line of output (design §4 "stream + take first line only"), driving
// the shared accumulator for TTFT stamping and the cutoff — same contract
// every other Provider adapter honors.
//
// It honors ctx throughout: the SDK builds its HTTP request with the ctx
// passed to NewStreaming, so cancelling ctx (e.g. because a newer keystroke
// superseded this request — see server.handle) aborts the call, including
// mid-stream reads.
func (c *anthropicClient) Complete(ctx context.Context, req Request) (Completion, error) {
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = c.maxTokens
	}

	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(c.model),
		MaxTokens: int64(maxTokens),
		// No CacheControl breakpoint on the system block: T4 measured the
		// system prompt at ~1171 tokens, well under Haiku 4.5's 4096-token
		// minimum cacheable prefix, so a cache_control here caches nothing
		// (cache_creation_input_tokens stays 0) — a silent no-op, not a win.
		// CachedTokens is still read below so the day a larger prefix or a
		// caching-capable model lands, the metric proves whether it hit.
		System: []anthropic.TextBlockParam{{Text: req.Prompt.System}},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(req.Prompt.ChatUser())),
		},
		// Deliberately NOT set: Thinking, OutputConfig.Effort, Temperature,
		// TopP. Haiku 4.5 doesn't think unless Thinking is explicitly enabled
		// (wrong for a sub-second completion), and `effort` errors outright on
		// Haiku 4.5 — see the ticket contract. Sampling params aren't needed
		// either; the system prompt already constrains output shape.
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
			// First-line cutoff (design §4) — the single most important
			// invariant in this file. The instant acc.Push reports the
			// accumulated text now contains a complete line, we stop
			// reading and return immediately. We must NOT keep draining the
			// stream to collect the trailing message_delta usage event: the
			// whole point of streaming is to avoid paying for the rest of
			// the completion once we already have a usable single-line
			// suggestion. defer stream.Close() above tears down the
			// in-flight SSE read on this early-return path.
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
			// METRICS(§12): usage on message_delta is cumulative for the
			// whole response so far, and stop_reason lands here too. Only
			// reached when the stream ends without a newline ever appearing
			// (the cutoff above returns first otherwise) — same trade-off
			// openai.go documents for its trailing usage chunk.
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
