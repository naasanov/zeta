// openai.go adapts the official openai-go v3 SDK to the Provider seam
// (design §6). Phase 1's hand-rolled SSE client is gone; the SDK now owns
// HTTP/SSE parsing. This adapter still covers the whole OpenAI-compatible
// ecosystem (Groq, OpenAI, Together, Ollama, ...) by swapping base URL +
// model + key — nothing Groq-specific is hardcoded, so portability is
// preserved even though the transport is no longer hand-rolled.
package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// openAIClient talks to a single OpenAI-compatible /chat/completions
// endpoint via the openai-go SDK client. It holds a shared *http.Client (via
// option.WithHTTPClient) so the daemon never pays TLS/TCP setup cost per
// keystroke (design §4 "warm connections") — construct one via NewOpenAI at
// startup and reuse it for every request.
type openAIClient struct {
	client    openai.Client
	model     string
	maxTokens int
}

// NewOpenAI builds a Provider backed by the openai-go SDK client, pointed at
// baseURL with apiKey. Call this once at daemon startup, not per-request: a
// fresh http.Client (and therefore a fresh connection pool) defeats the whole
// warm-connection point.
func NewOpenAI(baseURL, model, apiKey string, maxTokens int) (Provider, error) {
	transport := &http.Transport{
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        16,
		MaxIdleConnsPerHost: 16,
		IdleConnTimeout:     90 * time.Second,
	}
	httpClient := &http.Client{Transport: transport}

	client := openai.NewClient(
		option.WithBaseURL(baseURL),
		option.WithAPIKey(apiKey),
		option.WithHTTPClient(httpClient),
	)

	return &openAIClient{
		client:    client,
		model:     model,
		maxTokens: maxTokens,
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

// Complete issues a streaming chat-completions request via the SDK and
// returns the model's first line of output (design §4 "stream + take first
// line only"), driving the shared accumulator for TTFT stamping and the
// cutoff.
//
// It honors ctx throughout: ctx is passed straight into
// client.Chat.Completions.NewStreaming, so cancelling it (e.g. because a
// newer keystroke superseded this request — see server.handle) aborts the
// call, including mid-stream reads of the response body.
func (c *openAIClient) Complete(ctx context.Context, req Request) (Completion, error) {
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = c.maxTokens
	}

	params := openai.ChatCompletionNewParams{
		Model: c.model,
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(req.Prompt.System),
			openai.UserMessage(req.Prompt.ChatUser()),
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
		// empty there), decoded defensively via the respjson presence check
		// since CompletionUsage is a plain struct (not a pointer) and its
		// zero value is indistinguishable from "absent" otherwise. This check
		// must happen before the len==0 skip below.
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
		// ctx cancellation surfaces as the bare ctx.Err() from the SDK's
		// request loop (see requestconfig.RequestConfig.Execute) on the
		// initial round trip, but as an opaque wrapped "context canceled"
		// from the transport when it aborts a mid-stream body read instead.
		// Prefer ctx.Err() itself over the SDK's err in both cases so the
		// wrapped error is guaranteed to satisfy
		// errors.Is(err, context.Canceled).
		if ctx.Err() != nil {
			return Completion{}, &Error{Kind: ErrCanceled, Provider: c.Name(), Err: ctx.Err()}
		}

		// *openai.Error (an alias for the SDK's internal apierror.Error) is
		// what the SDK returns for any non-2xx response; it carries the
		// HTTP status code so callers can classify and log it.
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
