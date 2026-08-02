// Package prompt assembles the system/user turns sent to the LLM provider
// from a protocol.Request.
package prompt

import (
	"strconv"
	"strings"

	"github.com/naasanov/zsh-autopilot/daemon/internal/protocol"
)

// systemPrompt tells the model to emit only the text to append to the buffer.
// Typing completion and next-command prediction share it — next-command is
// just the append contract with an empty buffer. The reply is req.Buf +
// suffix with nothing inserted between, so the model must supply its own
// leading space (otherwise "git add" + "." => "git add."). Output must stay
// single-line; provider.Complete cuts at the first newline.
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

// User-turn directives stay outside the system prompt so typing and
// next-command can share one system prefix. Placing the spacing directive
// next to the input (not just in the system prompt) fixed instruct models
// dropping leading spaces.
const (
	typingUserPrefix      = "Complete this command, keeping any needed leading space:\n"
	nextCommandUserPrefix = "The prompt is empty. Based on the recent commands and context above, predict the single most likely next command. Keep it short and common:\n"
)

// RecentCommandsLabel prefixes the recent-commands line in contextBlock's
// output. Exported so callers rendering history separately can identify and
// skip that line rather than duplicating it.
const RecentCommandsLabel = "recent commands: "

// contextBlock renders whatever context fields are present on req into a
// compact "Context:" block, one line per present field, omitting lines for
// absent/zero fields. Returns "" when nothing is present.
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
		lines = append(lines, "- "+RecentCommandsLabel+strings.Join(req.History, "; "))
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
	System      string   // stable across every request — the prompt-cache anchor
	Instruction string   // typing vs next-command append contract; static per mode
	Context     string   // "Context:\n cwd: ...\n" — changes on chpwd/precmd
	Prefix      string   // the buffer being completed; may be ""
	Suffix      string   // always "" in Phase 2; the FIM infill hook
	History     []string // raw recent commands, oldest-first; FIM renders these as raw
	// command lines contiguous with Prefix, chat adapters keep them in Context instead.

	// LastExit is req.LastExit verbatim, including 0 — unlike Context, which
	// omits the exit line entirely at 0, so this is the only way to recover
	// the value once Context has dropped it.
	LastExit int
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
		History:     req.History,
		LastExit:    req.LastExit,
	}
}

// ChatUser renders the user turn for chat adapters: Context + Instruction +
// Prefix, with Prefix last so the completion continues directly from the
// buffer. Reordering to cache Instruction as a stable prefix was considered
// and dropped as unreachable (see CLAUDE.md "Prompt caching").
func (p Prompt) ChatUser() string {
	return p.Context + p.Instruction + p.Prefix
}
