// Package suggest adapts a provider.Provider into the server's suggest seam.
package suggest

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/naasanov/zsh-autopilot/daemon/internal/metrics"
	"github.com/naasanov/zsh-autopilot/daemon/internal/prompt"
	"github.com/naasanov/zsh-autopilot/daemon/internal/protocol"
	"github.com/naasanov/zsh-autopilot/daemon/internal/provider"
)

// LLM adapts a provider.Provider into the server's suggest seam
// (func(ctx, protocol.Request) (protocol.Reply, error)). It builds the
// prompt from req.Buf plus whatever step-5 context fields (design §7) are
// present on the request (see prompt.Build), calls the provider, and
// assembles the reply so Suggestion always starts with req.Buf: the zsh
// client strips that exact prefix before painting ghost text, so this
// invariant is load-bearing, not cosmetic.
//
// Taking provider.Provider (the interface) rather than a concrete client is
// the point of this seam: it makes LLM testable with a stub instead of an
// httptest.Server (see TestLLM_StubProvider).
//
// METRICS(§12): emit, when non-nil, receives the "request" event built from
// req + the provider's Completion stats after every call (success or
// error). nil disables metrics entirely with zero added cost on this path.
//
// METRICS(§12): rawText gates opt-in raw-text capture (design §12; see
// metrics.Config.RawText). When true, the emitted event's raw-text fields
// (Buf/Suggestion/Cwd/GitBranch/GitDirty/LastExit/History/DirEntries) are
// filled from req (and, on success, reply.Suggestion) so the event alone is
// enough to reconstruct the originating protocol.Request for eval-harness
// replay. When false, none of those fields are touched — they stay zero and
// omitempty, so the emitted JSON is unchanged from before this parameter
// existed. This is the flag the Phase-3 metrics strip should grep for
// (METRICS(§12)) alongside everything else in this package.
func LLM(p provider.Provider, log *slog.Logger, emit func(metrics.RequestEvent), rawText bool) func(ctx context.Context, req protocol.Request) (protocol.Reply, error) {
	return func(ctx context.Context, req protocol.Request) (protocol.Reply, error) {
		built := prompt.Build(req)

		// METRICS(§12): suggest_ms is wall time around the provider call.
		start := time.Now()
		completion, err := p.Complete(ctx, provider.Request{Prompt: built})
		suggestMs := float64(time.Since(start)) / float64(time.Millisecond)

		if err != nil {
			// METRICS(§12): error_type unwraps a *provider.Error to its Kind;
			// empty string when the error isn't a *provider.Error (e.g. a
			// bare ctx.Err() from somewhere else in the stack).
			var errorType string
			var perr *provider.Error
			if errors.As(err, &perr) {
				errorType = string(perr.Kind)
			}

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
					Provider:          p.Name(),
					Model:             p.Model(),
					ErrorType:         errorType,
					PriceTableVersion: metrics.PriceTableVersion,
				}
				if ctx.Err() != nil {
					ev.Cancelled = true
					ev.CancelledAtStage = "in_flight"
				}
				// METRICS(§12): raw-text capture, error path — no Suggestion
				// exists (the call failed), but the request-side fields are
				// still filled: a failed/cancelled request is still a valid
				// eval-harness input to replay.
				if rawText {
					ev.Buf = req.Buf
					ev.Cwd = req.Cwd
					ev.GitBranch = req.GitBranch
					ev.GitDirty = req.GitDirty
					ev.LastExit = req.LastExit
					ev.History = req.History
					ev.DirEntries = req.DirEntries
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
			// METRICS(§12): raw-text capture, success path — the full
			// reply.Suggestion (which by contract starts with req.Buf) is
			// captured alongside the request-side fields, enough to
			// reconstruct req verbatim and replay it as an eval case.
			if rawText {
				ev.Buf = req.Buf
				ev.Suggestion = reply.Suggestion
				ev.Cwd = req.Cwd
				ev.GitBranch = req.GitBranch
				ev.GitDirty = req.GitDirty
				ev.LastExit = req.LastExit
				ev.History = req.History
				ev.DirEntries = req.DirEntries
			}
			emit(ev)
		}

		return reply, nil
	}
}
