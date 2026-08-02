// Package prompt owns content selection and rendering for every prompt this
// daemon can send a provider. A prompt renders directly from a
// protocol.Request and implements ChatPrompt, FIMPrompt, or both.
package prompt

import "github.com/naasanov/zsh-autopilot/daemon/internal/protocol"

// Prompt is the base type every registered prompt satisfies.
type Prompt interface{ Name() string }

// ChatPayload is the rendered chat turn pair for a chat-shaped adapter
// (anthropic, openai-compatible).
type ChatPayload struct{ System, User string }

// FIMPayload is the rendered prefix/suffix pair for a fill-in-the-middle
// adapter (codestral). Suffix is always "" today: protocol.Request carries
// no cursor position.
type FIMPayload struct{ Prefix, Suffix string }

// ChatPrompt renders for chat-shaped adapters.
type ChatPrompt interface {
	Prompt
	RenderChat(protocol.Request) ChatPayload
}

// FIMPrompt renders for fill-in-the-middle adapters.
type FIMPrompt interface {
	Prompt
	RenderFIM(protocol.Request) FIMPayload
}
