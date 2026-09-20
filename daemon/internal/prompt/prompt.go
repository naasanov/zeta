// Package prompt owns content selection and rendering for every prompt sent to a provider.
package prompt

import "github.com/naasanov/zsh-autopilot/daemon/internal/protocol"

type Prompt interface{ Name() string }

// ChatPayload is the rendered chat turn pair for a chat-shaped adapter
// (anthropic, openai-compatible).
type ChatPayload struct{ System, User string }

// FIMPayload is the rendered prefix/suffix pair for a fill-in-the-middle
// adapter (codestral). Suffix is always "" today: protocol.Request carries
// no cursor position.
type FIMPayload struct{ Prefix, Suffix string }

type ChatPrompt interface {
	Prompt
	RenderChat(protocol.Request) ChatPayload
}

type FIMPrompt interface {
	Prompt
	RenderFIM(protocol.Request) FIMPayload
}
