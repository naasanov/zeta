// Package suggest adapts a provider.Provider into the server's suggest seam.
package suggest

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/naasanov/zsh-autopilot/daemon/internal/metrics"
	"github.com/naasanov/zsh-autopilot/daemon/internal/protocol"
	"github.com/naasanov/zsh-autopilot/daemon/internal/provider"
)

// LLM adapts a provider.Provider into the server's suggest seam. Suggestion
// always starts with req.Buf; the zsh client strips that exact prefix
// before painting ghost text, so this is load-bearing.
func LLM(p provider.Provider, log *slog.Logger, emit func(metrics.RequestEvent), rawText bool) func(ctx context.Context, req protocol.Request) (protocol.Reply, error) {
	return func(ctx context.Context, req protocol.Request) (protocol.Reply, error) {
		// suggestMs is wall time around the provider call.
		start := time.Now()
		completion, err := p.Complete(ctx, provider.Request{Req: req})
		suggestMs := float64(time.Since(start)) / float64(time.Millisecond)

		if err != nil {
			// errorType unwraps a *provider.Error to its Kind; empty when err
			// isn't a *provider.Error (e.g. a bare ctx.Err()).
			var errorType string
			var perr *provider.Error
			if errors.As(err, &perr) {
				errorType = string(perr.Kind)
			}

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
					Provider:          p.Name(),
					Model:             p.Model(),
					ErrorType:         errorType,
					PriceTableVersion: metrics.PriceTableVersion,
				}
				// A cancelled/superseded request gets its own event shape,
				// distinguishable from a real provider error.
				if ctx.Err() != nil {
					ev.Cancelled = true
					ev.CancelledAtStage = "in_flight"
				}
				// Error path: no Suggestion since the call failed, but
				// request-side fields are still filled for eval replay.
				if rawText {
					ev.Buf = req.Buf
					ev.Cwd = req.Cwd
					ev.GitBranch = req.GitBranch
					ev.GitDirty = req.GitDirty
					ev.LastExit = req.LastExit
					ev.History = req.History
					ev.HistoryCwds = req.HistoryCwds
					ev.DirEntries = req.DirEntries
				}
				emit(ev)
			}
			return protocol.Reply{}, err
		}
		suffix := strings.TrimRight(completion.Text, " \t\r\n")

		reply := protocol.Reply{
			V:          protocol.Version,
			ID:         req.ID,
			Source:     protocol.SourceLLM,
			Suggestion: req.Buf + suffix,
		}

		if emit != nil {
			ev := metrics.RequestEvent{
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
				Provider:          p.Name(),
				Model:             p.Model(),
				CostUSD:           metrics.CostUSD(p.Name(), p.Model(), completion.InputTokens, completion.OutputTokens, completion.CachedTokens),
				PriceTableVersion: metrics.PriceTableVersion,
			}
			// Success path fills reply.Suggestion plus request-side fields,
			// enough to replay req as an eval case.
			if rawText {
				ev.Buf = req.Buf
				ev.Suggestion = reply.Suggestion
				ev.Cwd = req.Cwd
				ev.GitBranch = req.GitBranch
				ev.GitDirty = req.GitDirty
				ev.LastExit = req.LastExit
				ev.History = req.History
				ev.HistoryCwds = req.HistoryCwds
				ev.DirEntries = req.DirEntries
			}
			emit(ev)
		}

		return reply, nil
	}
}
