package prompt

import (
	"fmt"
	"sort"
	"strings"
	"text/template"

	"github.com/naasanov/zsh-autopilot/daemon/internal/protocol"
)

var funcs = template.FuncMap{"join": strings.Join}

// mustRender executes t into a string. Execute only fails on a template bug
// (bad field name, wrong type), never on input data, so a failure here is
// something to panic on, not propagate.
func mustRender(t *template.Template, data any) string {
	var b strings.Builder
	if err := t.Execute(&b, data); err != nil {
		panic(fmt.Sprintf("prompt: template %s: %v", t.Name(), err))
	}
	return b.String()
}

// mustParse parses text as a named template with the shared funcs. A parse
// error is a programming bug, so it panics at init rather than at render.
func mustParse(name, text string) *template.Template {
	return template.Must(template.New(name).Funcs(funcs).Parse(text))
}

// A view struct embeds protocol.Request (so fields like .Cwd/.History/.Buf
// keep resolving via promotion) and adds fields a template needs that
// protocol.Request doesn't carry — e.g. a precomputed bool a template can't
// derive itself. Declare it anonymously at the render site so it stays
// private to that one prompt; chat-append has two examples.

// Ambient context renders as "#"-comments (FIM takes no system role), then
// recent history, then the buffer/cursor line. History renders contiguous
// with the buffer — shell history is valid shell code, so a code model
// continues it as a transcript. Each template below is a complete, standalone
// experiment; the repetition between them is deliberate.

// all is the stable, ordered list of every registered prompt. A slice, not a
// map, so All() and the error message in ByName have a fixed, reproducible
// order rather than depending on map iteration.
var all = []Prompt{
	chatAppend,
	fimTranscriptMarker,
	fimCommentedHistory,
	fimExitCodeAlways,
	fimNoMarker,
	fimGuardComment,
}

// All returns every registered prompt, in stable order.
func All() []Prompt {
	out := make([]Prompt, len(all))
	copy(out, all)
	return out
}

// ByName looks up a registered prompt by name, case-insensitively. The error
// lists every valid name so a caller (e.g. config validation) can print a
// complete, actionable message.
func ByName(name string) (Prompt, error) {
	for _, p := range all {
		if strings.EqualFold(p.Name(), name) {
			return p, nil
		}
	}
	names := make([]string, len(all))
	for i, p := range all {
		names[i] = p.Name()
	}
	sort.Strings(names)
	return nil, fmt.Errorf("prompt: unknown name %q, valid names are: %s", name, strings.Join(names, ", "))
}

// ShippedFor returns the default prompt for a given provider adapter
// ("openai", "anthropic", "codestral"). Panics on an unrecognized adapter —
// every adapter this daemon ships is known at compile time.
func ShippedFor(adapter string) Prompt {
	switch adapter {
	case "openai", "anthropic":
		return chatAppend
	case "codestral":
		return fimTranscriptMarker
	default:
		panic(fmt.Sprintf("prompt: ShippedFor: unknown adapter %q", adapter))
	}
}

// =============================== chat-append ==============================

// chatAppendPrompt is "chat-append", used by the anthropic and
// openai-compatible adapters.
type chatAppendPrompt struct{}

var chatAppend = chatAppendPrompt{}

func (chatAppendPrompt) Name() string { return "chat-append" }

// chatSystemPrompt tells the model to emit only the text to append to the
// buffer; the reply is req.Buf + output with nothing inserted between, so
// the model must supply its own leading space (else "git add"+"." =>
// "git add."). Output must stay single-line. Do NOT strip the few-shot
// examples below: instruct models drop the leading space without them.
const chatSystemPrompt = `You are a shell command suggestion engine. You receive a shell command buffer and output only the text to append at the end — nothing else. When the buffer is empty, the appended text may be a complete next command.

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

var chatContextTmpl = mustParse("chat-append/context", `
{{- if .HasContext}}Context:
{{- if .Cwd}}
- cwd: {{.Cwd}}{{end}}
{{- if .DirEntries}}
- files: {{join .DirEntries " "}}{{end}}
{{- if .GitBranch}}
- git: branch {{.GitBranch}}{{if .GitDirty}} (dirty){{end}}{{end}}
{{- if .LastExit}}
- last command failed (exit {{.LastExit}}){{end}}
{{- if .History}}
- recent commands: {{join .History "; "}}{{end}}{{print "\n\n"}}{{end -}}`)

// contextBlock renders whatever context fields are present on req into a
// compact "Context:" block, one line per present field, omitting lines for
// absent/zero fields. Returns "" when nothing is present.
//
// HasContext gates the "Context:" header and its trailing blank line as a
// single unit, which no per-field conditional can express.
func contextBlock(req protocol.Request) string {
	view := struct {
		protocol.Request
		HasContext bool
	}{
		Request: req,
		HasContext: req.Cwd != "" || len(req.DirEntries) > 0 || req.GitBranch != "" ||
			req.LastExit != 0 || len(req.History) > 0,
	}
	return mustRender(chatContextTmpl, view)
}

var chatUserTmpl = mustParse("chat-append/user",
	`{{.Context}}{{if .IsNextCommand}}The prompt is empty. Based on the recent commands and context above, predict the single most likely next command. Keep it short and common:
{{else}}Complete this command, keeping any needed leading space:
{{end}}{{.Buf}}`)

// RenderChat renders the system/user turn pair. The user turn is Context +
// Instruction + Prefix (the buffer), with Prefix last so the completion
// continues directly from the buffer.
func (chatAppendPrompt) RenderChat(req protocol.Request) ChatPayload {
	view := struct {
		protocol.Request
		Context       string
		IsNextCommand bool
	}{
		Request:       req,
		Context:       contextBlock(req),
		IsNextCommand: req.Kind == protocol.KindNextCommand,
	}
	return ChatPayload{System: chatSystemPrompt, User: mustRender(chatUserTmpl, view)}
}

// ======================== fim-transcript-marker ===========================

// fimTranscriptMarkerPrompt is "fim-transcript-marker", the shipped FIM
// default.
type fimTranscriptMarkerPrompt struct{}

var fimTranscriptMarker = fimTranscriptMarkerPrompt{}

func (fimTranscriptMarkerPrompt) Name() string { return "fim-transcript-marker" }

var fimTranscriptMarkerTmpl = mustParse("fim-transcript-marker", `
{{- if .Cwd}}# cwd: {{.Cwd}}
{{end}}
{{- if .DirEntries}}# files: {{join .DirEntries " "}}
{{end}}
{{- if .GitBranch}}# git: branch {{.GitBranch}}{{if .GitDirty}} (dirty){{end}}
{{end}}
{{- if .LastExit}}# last command failed (exit {{.LastExit}})
{{end}}
{{- range .History}}$ {{.}}
{{end}}
{{- print "$ "}}{{.Buf}}`)

// RenderFIM prefixes every command line, history and cursor alike, with a
// "$ " transcript marker — without it the model completes the last history
// line instead of predicting a new one.
func (fimTranscriptMarkerPrompt) RenderFIM(req protocol.Request) FIMPayload {
	return FIMPayload{Prefix: mustRender(fimTranscriptMarkerTmpl, req)}
}

// ======================== fim-commented-history ===========================

// fimCommentedHistoryPrompt is "fim-commented-history": ambient context
// comments, then history rendered as "# <cmd>" comment lines, then the
// marker + buffer. Tests commented-vs-raw history against
// fim-transcript-marker.
type fimCommentedHistoryPrompt struct{}

var fimCommentedHistory = fimCommentedHistoryPrompt{}

func (fimCommentedHistoryPrompt) Name() string { return "fim-commented-history" }

var fimCommentedHistoryTmpl = mustParse("fim-commented-history", `
{{- if .Cwd}}# cwd: {{.Cwd}}
{{end}}
{{- if .DirEntries}}# files: {{join .DirEntries " "}}
{{end}}
{{- if .GitBranch}}# git: branch {{.GitBranch}}{{if .GitDirty}} (dirty){{end}}
{{end}}
{{- if .LastExit}}# last command failed (exit {{.LastExit}})
{{end}}
{{- range .History}}# {{.}}
{{end}}
{{- print "$ "}}{{.Buf}}`)

func (fimCommentedHistoryPrompt) RenderFIM(req protocol.Request) FIMPayload {
	return FIMPayload{Prefix: mustRender(fimCommentedHistoryTmpl, req)}
}

// ======================== fim-exit-code-always ============================

// fimExitCodeAlwaysPrompt is "fim-exit-code-always": the shipped shape plus
// an "# exit: N" line (even when N is 0) between history and the cursor.
type fimExitCodeAlwaysPrompt struct{}

var fimExitCodeAlways = fimExitCodeAlwaysPrompt{}

func (fimExitCodeAlwaysPrompt) Name() string { return "fim-exit-code-always" }

var fimExitCodeAlwaysTmpl = mustParse("fim-exit-code-always", `
{{- if .Cwd}}# cwd: {{.Cwd}}
{{end}}
{{- if .DirEntries}}# files: {{join .DirEntries " "}}
{{end}}
{{- if .GitBranch}}# git: branch {{.GitBranch}}{{if .GitDirty}} (dirty){{end}}
{{end}}
{{- if .LastExit}}# last command failed (exit {{.LastExit}})
{{end}}
{{- range .History}}$ {{.}}
{{end}}
{{- print "# exit: "}}{{.LastExit}}
{{- print "\n$ "}}{{.Buf}}`)

func (fimExitCodeAlwaysPrompt) RenderFIM(req protocol.Request) FIMPayload {
	return FIMPayload{Prefix: mustRender(fimExitCodeAlwaysTmpl, req)}
}

// ============================ fim-no-marker ===============================

// fimNoMarkerPrompt is "fim-no-marker", the pre-marker shape, kept
// registered as a known-losing baseline.
type fimNoMarkerPrompt struct{}

var fimNoMarker = fimNoMarkerPrompt{}

func (fimNoMarkerPrompt) Name() string { return "fim-no-marker" }

var fimNoMarkerTmpl = mustParse("fim-no-marker", `
{{- if .Cwd}}# cwd: {{.Cwd}}
{{end}}
{{- if .DirEntries}}# files: {{join .DirEntries " "}}
{{end}}
{{- if .GitBranch}}# git: branch {{.GitBranch}}{{if .GitDirty}} (dirty){{end}}
{{end}}
{{- if .LastExit}}# last command failed (exit {{.LastExit}})
{{end}}
{{- range .History}}{{.}}
{{end}}
{{- print ""}}{{.Buf}}`)

func (fimNoMarkerPrompt) RenderFIM(req protocol.Request) FIMPayload {
	return FIMPayload{Prefix: mustRender(fimNoMarkerTmpl, req)}
}

// ========================= fim-guard-comment ==============================

// fimGuardCommentPrompt is "fim-guard-comment": the shipped shape plus a
// fixed rules block up top, testing whether a base completion model honors an
// instruction it can only receive as shell comments (FIM takes no system
// role). Codestral fabricates specifics it cannot know — remote URLs,
// hostnames, commit messages — most often when context is thin.
//
// The rules go ABOVE the ambient context, never between the last history
// line and the cursor: that adjacency is what makes the model continue the
// transcript rather than complete the previous line.
//
// The failure mode to watch is the model continuing the comment block instead
// of emitting a command; the "$ " marker is what should pull it back.
type fimGuardCommentPrompt struct{}

var fimGuardComment = fimGuardCommentPrompt{}

func (fimGuardCommentPrompt) Name() string { return "fim-guard-comment" }

// The first conditional is `{{if}}`, not `{{- if}}` as in every other
// template here: the dash trims ALL preceding whitespace, which would eat the
// newline ending the rules block and run it into the cwd line.
var fimGuardCommentTmpl = mustParse("fim-guard-comment", `# shell transcript - predict only the next command
# never invent a name that does not appear above: commit messages,
# branch names, file names, hostnames, URLs, values
# stop before free-form input, e.g. end at the opening quote
# a short, partial completion is better than a confident guess
{{if .Cwd}}# cwd: {{.Cwd}}
{{end}}
{{- if .DirEntries}}# files: {{join .DirEntries " "}}
{{end}}
{{- if .GitBranch}}# git: branch {{.GitBranch}}{{if .GitDirty}} (dirty){{end}}
{{end}}
{{- if .LastExit}}# last command failed (exit {{.LastExit}})
{{end}}
{{- range .History}}$ {{.}}
{{end}}
{{- print "$ "}}{{.Buf}}`)

func (fimGuardCommentPrompt) RenderFIM(req protocol.Request) FIMPayload {
	return FIMPayload{Prefix: mustRender(fimGuardCommentTmpl, req)}
}
