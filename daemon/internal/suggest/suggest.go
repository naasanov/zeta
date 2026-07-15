// Package suggest adapts a provider.Client into the server's suggest seam.
package suggest

import (
	"context"
	"log/slog"
	"strings"

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
func LLM(client *provider.Client, log *slog.Logger) func(ctx context.Context, req protocol.Request) (protocol.Reply, error) {
	return func(ctx context.Context, req protocol.Request) (protocol.Reply, error) {
		system, user := prompt.Build(req)

		suffix, err := client.Complete(ctx, system, user)
		if err != nil {
			// Coordinator logs and skips the write on error — graceful
			// degradation, no ghost text for this request.
			return protocol.Reply{}, err
		}
		suffix = strings.TrimRight(suffix, " \t\r\n")

		return protocol.Reply{
			V:          protocol.Version,
			ID:         req.ID,
			Source:     protocol.SourceLLM,
			Suggestion: req.Buf + suffix,
		}, nil
	}
}
