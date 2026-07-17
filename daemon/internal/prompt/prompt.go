// Package prompt assembles the system/user turns sent to the LLM provider
// from a protocol.Request.
package prompt

import (
	"strconv"
	"strings"

	"github.com/naasanov/zsh-autopilot/daemon/internal/protocol"
)

// systemPrompt tells the model to emit only the text to append to the command
// buffer. Typing completion and next-command prediction intentionally share
// this one stable system prompt: next-command is just the append contract with
// an empty buffer. Two properties are load-bearing and tuned by dogfooding
// (design §235):
//   - Spacing: the reply is req.Buf + suffix with nothing inserted between, so
//     the model must supply the leading space itself when the completion starts
//     a new word (otherwise "git add" + "." => "git add.").
//   - Restraint: prefer a short completion and stop before free-form input it
//     cannot know (a commit message, a filename) instead of fabricating one. A
//     partial completion is useful; the whole command is not required.
//
// The model's OUTPUT must stay single-line (provider.Complete cuts at the first
// newline). The prompt itself may span multiple lines. The « » in the examples
// only mark exact output boundaries so leading spaces are visible.
const systemPrompt = `You are a shell command suggestion engine. You receive a shell command buffer and output only the text to append at the end — nothing else. When the buffer is empty, the appended text may be a complete next command.

Rules:
- Your output is appended verbatim, with NO separator added. Begin with a space when the completion starts a new word or argument; begin with no space when finishing the current word.
- Never repeat or restate a non-empty buffer.
- Prefer a SHORT, high-confidence completion. A partial completion is useful — you do NOT need to produce the whole command.
- Never invent specifics you cannot know: commit messages, file names, branch names, URLs, values. Stop right before such free-form input (for example, end at the opening quote).
- Output a single line. No explanation, no markdown, no backticks.
- If nothing useful comes to mind, output nothing.

Examples — everything after the arrow indicates the exact output (leading spaces included)
git ad =>d
git add => .
git add -A => && git commit -m "
git commit -m  => "
docker run => -it `

// User-turn directives are deliberately outside the system prompt so typing and
// next-command can share one cacheable system prefix. For typing, placing the
// spacing directive right next to the input gives it more attention on instruct
// models than the system prompt alone — empirically this fixed the model
// dropping leading spaces. req.Buf stays at the very end so the completion
// continues directly from it, and the static prefix here is still a stable cache
// prefix for Phase 2 prompt caching.
const (
	typingUserPrefix      = "Complete this command, keeping any needed leading space:\n"
	nextCommandUserPrefix = "The prompt is empty. Based on the recent commands and context above, predict the single most likely next command. Keep it short and common:\n"
)

// contextBlock renders whatever step-5 context fields (design §7) are present
// on req into a compact "Context:" block for the user turn, one line per
// present field, omitting lines for absent/zero fields entirely (no "git:"
// line without a branch, no "last command" line when LastExit == 0, no
// "recent commands" line with empty History, no "files:" line with empty
// DirEntries). It returns "" when no context fields are present at all, so
// callers can skip the block cleanly.
//
// This is a pure function (no I/O) so it's unit-testable without a running
// daemon or provider.
func contextBlock(req protocol.Request) string {
	var lines []string
	if req.Cwd != "" {
		lines = append(lines, "- cwd: "+req.Cwd)
	}
	if len(req.DirEntries) > 0 {
		lines = append(lines, "- files: "+strings.Join(req.DirEntries, " "))
	}
	if req.GitBranch != "" {
		branch := "- git: branch " + req.GitBranch
		if req.GitDirty {
			branch += " (dirty)"
		}
		lines = append(lines, branch)
	}
	if req.LastExit != 0 {
		lines = append(lines, "- last command failed (exit "+strconv.Itoa(req.LastExit)+")")
	}
	if len(req.History) > 0 {
		lines = append(lines, "- recent commands: "+strings.Join(req.History, "; "))
	}
	if len(lines) == 0 {
		return ""
	}
	return "Context:\n" + strings.Join(lines, "\n") + "\n\n"
}

// Prompt is the provider-neutral prompt. Adapters render it their own way:
// chat adapters build messages (System + ChatUser()), the FIM adapter builds
// prompt+suffix directly from Prefix/Suffix.
type Prompt struct {
	System      string // stable across every request — the prompt-cache anchor
	Instruction string // typing vs next-command append contract; static per mode
	Context     string // "Context:\n cwd: ...\n" — changes on chpwd/precmd
	Prefix      string // the buffer being completed; may be ""
	Suffix      string // always "" in Phase 2; the FIM infill hook
}

// Build assembles the provider-neutral Prompt for a request. The system turn
// is intentionally mode-independent; KindTyping and KindNextCommand differ
// only in the short user-turn directive next to the buffer.
func Build(req protocol.Request) Prompt {
	instruction := typingUserPrefix
	if req.Kind == protocol.KindNextCommand {
		instruction = nextCommandUserPrefix
	}
	return Prompt{
		System:      systemPrompt,
		Instruction: instruction,
		Context:     contextBlock(req),
		Prefix:      req.Buf,
		Suffix:      "",
	}
}

// ChatUser renders the user turn for chat adapters: Context + Instruction +
// Prefix, with Prefix last so the completion continues directly from the
// buffer.
//
// T4 considered reordering this to Instruction-before-Context so a stable
// instruction prefix could seed a provider prompt cache (design §7). Measured
// and DROPPED: prompt caching is unreachable for every current profile — the
// full chat prefix (system prompt ~1171 tokens + this user turn) is far below
// Anthropic Haiku's 4096-token minimum cacheable prefix, Groq's default llama
// doesn't support caching, and the default provider (codestral, FIM) doesn't
// use ChatUser at all. Reorder only becomes worthwhile alongside a
// caching-capable chat model whose prefix clears its minimum; see the note in
// anthropic.go where a cache_control breakpoint would otherwise go.
func (p Prompt) ChatUser() string {
	return p.Context + p.Instruction + p.Prefix
}
