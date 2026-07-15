// Package suggest adapts a provider.Client into the server's suggest seam.
package suggest

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/naasanov/zsh-autopilot/daemon/internal/metrics"
	"github.com/naasanov/zsh-autopilot/daemon/internal/prompt"
	"github.com/naasanov/zsh-autopilot/daemon/internal/protocol"
	"github.com/naasanov/zsh-autopilot/daemon/internal/provider"
)

// LLM adapts a provider.Client into the server's suggest seam
// (func(ctx, protocol.Request) (protocol.Reply, error)). It builds the
// prompt from req.Buf plus whatever step-5 context fields (design §7) are
// present on the request (see prompt.Build), calls the provider, and
// assembles the reply so Suggestion always starts with req.Buf: the zsh
// client strips that exact prefix before painting ghost text, so this
// invariant is load-bearing, not cosmetic.
//
// METRICS(§12): emit, when non-nil, receives the "request" event built from
// req + the provider's Completion stats after every call (success or
// error). nil disables metrics entirely with zero added cost on this path.
func LLM(client *provider.Client, log *slog.Logger, emit func(metrics.RequestEvent)) func(ctx context.Context, req protocol.Request) (protocol.Reply, error) {
	return func(ctx context.Context, req protocol.Request) (protocol.Reply, error) {
		system, user := prompt.Build(req)

		// METRICS(§12): suggest_ms is wall time around the provider call.
		start := time.Now()
		completion, err := client.Complete(ctx, system, user)
		suggestMs := float64(time.Since(start)) / float64(time.Millisecond)

		if err != nil {
			// METRICS(§12): a cancelled/superseded request (ctx.Err() != nil)
			// gets its own event shape so cancellations are distinguishable
			// from real provider errors in the log.
			if emit != nil {
				ev := metrics.RequestEvent{
					V:                 1,
					Event:             "request",
					TS:                float64(time.Now().UnixNano()) / 1e9,
					SessionID:         metrics.SessionID(req.ID),
					RequestID:         req.ID,
					Trigger:           req.Kind,
					BufferLen:         len(req.Buf),
					SuggestMs:         suggestMs,
					HTTPStatus:        completion.HTTPStatus,
					PriceTableVersion: metrics.PriceTableVersion,
				}
				if ctx.Err() != nil {
					ev.Cancelled = true
					ev.CancelledAtStage = "in_flight"
				}
				emit(ev)
			}
			// Coordinator logs and skips the write on error — graceful
			// degradation, no ghost text for this request.
			return protocol.Reply{}, err
		}
		suffix := strings.TrimRight(completion.Text, " \t\r\n")

		reply := protocol.Reply{
			V:          protocol.Version,
			ID:         req.ID,
			Source:     protocol.SourceLLM,
			Suggestion: req.Buf + suffix,
		}

		// METRICS(§12): build + emit the "request" event on the success path.
		if emit != nil {
			emit(metrics.RequestEvent{
				V:                 1,
				Event:             "request",
				TS:                float64(time.Now().UnixNano()) / 1e9,
				SessionID:         metrics.SessionID(req.ID),
				RequestID:         req.ID,
				Trigger:           req.Kind,
				BufferLen:         len(req.Buf),
				SuggestionLen:     len(reply.Suggestion),
				Source:            reply.Source,
				TTFTMs:            float64(completion.TTFT) / float64(time.Millisecond),
				SuggestMs:         suggestMs,
				InputTokens:       completion.InputTokens,
				OutputTokens:      completion.OutputTokens,
				CachedReadTokens:  completion.CachedTokens,
				HTTPStatus:        completion.HTTPStatus,
				StopReason:        completion.StopReason,
				CostUSD:           metrics.CostUSD(client.Model(), completion.InputTokens, completion.OutputTokens, completion.CachedTokens),
				PriceTableVersion: metrics.PriceTableVersion,
			})
		}

		return reply, nil
	}
}
