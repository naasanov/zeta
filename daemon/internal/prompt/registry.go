package prompt

import (
	"fmt"
	"sort"
	"strings"
	"text/template"

	"github.com/naasanov/zsh-autopilot/daemon/internal/protocol"
)

var funcs = template.FuncMap{"join": strings.Join}

const defaultHistoryN = 30

func tailHistory(req protocol.Request, n int) protocol.Request {
	entries := req.HistoryWithCwd()
	if len(entries) > n {
		entries = entries[len(entries)-n:]
	}
	req.SetHistoryEntries(entries)
	return req
}

func mustRender(t *template.Template, data any) string {
	var b strings.Builder
	if err := t.Execute(&b, data); err != nil {
		panic(fmt.Sprintf("prompt: template %s: %v", t.Name(), err))
	}
	return b.String()
}

func mustParse(name, text string) *template.Template {
	return template.Must(template.New(name).Funcs(funcs).Parse(text))
}

var all = []Prompt{
	chatAppend,
	cwdGrouped,
	fimTranscriptMarker,
	cwdFiltered,
	fimTranscriptMarker10,
	fimTranscriptMarker20,
	fimGuardComment,
	fimCommentedHistory,
	fimExitCodeAlways,
	fimNoMarker,
}

func All() []Prompt {
	out := make([]Prompt, len(all))
	copy(out, all)
	return out
}

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

func ShippedFor(adapter string) Prompt {
	switch adapter {
	case "openai", "anthropic":
		return chatAppend
	case "codestral":
		return cwdGrouped
	default:
		panic(fmt.Sprintf("prompt: ShippedFor: unknown adapter %q", adapter))
	}
}

// =============================== chat-append ==============================

type chatAppendPrompt struct{}

var chatAppend = chatAppendPrompt{}

func (chatAppendPrompt) Name() string { return "chat-append" }

// The few-shot examples below are load-bearing; instruct models drop the
// leading space without them.
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

func (chatAppendPrompt) RenderChat(req protocol.Request) ChatPayload {
	req = tailHistory(req, defaultHistoryN)
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

type fimTranscriptMarkerPrompt struct{ n int }

var (
	fimTranscriptMarker   = fimTranscriptMarkerPrompt{n: defaultHistoryN}
	fimTranscriptMarker10 = fimTranscriptMarkerPrompt{n: 10}
	fimTranscriptMarker20 = fimTranscriptMarkerPrompt{n: 20}
)

func (p fimTranscriptMarkerPrompt) Name() string {
	if p.n == defaultHistoryN {
		return "fim-transcript-marker"
	}
	return fmt.Sprintf("fim-transcript-marker-%d", p.n)
}

var fimTranscriptMarkerTmpl = mustParse("fim-transcript-marker", `
{{- if .Cwd}}# cwd: {{.Cwd}}
{{end}}
{{- if .DirEntries}}# files: {{join .DirEntries " "}}
{{end}}
{{- if .GitBranch}}# git: branch {{.GitBranch}}{{if .GitDirty}} (dirty){{end}}
{{end}}
{{- range .History}}$ {{.}}
{{end}}
{{- if .LastExit}}# last command failed (exit {{.LastExit}})
{{end}}
{{- print "$ "}}{{.Buf}}`)

func (p fimTranscriptMarkerPrompt) RenderFIM(req protocol.Request) FIMPayload {
	req = tailHistory(req, p.n)
	return FIMPayload{Prefix: mustRender(fimTranscriptMarkerTmpl, req)}
}

// ======================== fim-commented-history ===========================

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
	req = tailHistory(req, defaultHistoryN)
	return FIMPayload{Prefix: mustRender(fimCommentedHistoryTmpl, req)}
}

// ======================== fim-exit-code-always ============================

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
	req = tailHistory(req, defaultHistoryN)
	return FIMPayload{Prefix: mustRender(fimExitCodeAlwaysTmpl, req)}
}

// ============================ fim-no-marker ===============================

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
{{- range .History}}{{.}}
{{end}}
{{- if .LastExit}}# last command failed (exit {{.LastExit}})
{{end}}
{{- print ""}}{{.Buf}}`)

func (fimNoMarkerPrompt) RenderFIM(req protocol.Request) FIMPayload {
	req = tailHistory(req, defaultHistoryN)
	return FIMPayload{Prefix: mustRender(fimNoMarkerTmpl, req)}
}

// ========================= fim-guard-comment ==============================

type fimGuardCommentPrompt struct{}

var fimGuardComment = fimGuardCommentPrompt{}

func (fimGuardCommentPrompt) Name() string { return "fim-guard-comment" }

// The first conditional is `{{if}}`, not `{{- if}}`: the dash would eat the newline before the cwd line.
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
	req = tailHistory(req, defaultHistoryN)
	return FIMPayload{Prefix: mustRender(fimGuardCommentTmpl, req)}
}

// ============================= cwd-filtered ================================

type cwdFilteredPrompt struct{}

var cwdFiltered = cwdFilteredPrompt{}

var _ interface {
	ChatPrompt
	FIMPrompt
} = cwdFiltered

func (cwdFilteredPrompt) Name() string { return "cwd-filtered" }

func (cwdFilteredPrompt) partition(req protocol.Request) (filtered protocol.Request, active bool) {
	if req.Cwd == "" || !req.HasHistoryCwd() {
		return req, false
	}
	var matches []protocol.HistoryEntry
	for _, e := range req.HistoryWithCwd() {
		if e.Cwd == req.Cwd {
			matches = append(matches, e)
		}
	}
	if len(matches) > defaultHistoryN {
		matches = matches[len(matches)-defaultHistoryN:]
	}
	req.SetHistoryEntries(matches)
	return req, true
}

func (p cwdFilteredPrompt) RenderChat(req protocol.Request) ChatPayload {
	filtered, active := p.partition(req)
	if !active {
		return chatAppend.RenderChat(req)
	}
	view := struct {
		protocol.Request
		Context       string
		IsNextCommand bool
	}{
		Request:       filtered,
		Context:       contextBlock(filtered),
		IsNextCommand: filtered.Kind == protocol.KindNextCommand,
	}
	return ChatPayload{System: chatSystemPrompt, User: mustRender(chatUserTmpl, view)}
}

func (p cwdFilteredPrompt) RenderFIM(req protocol.Request) FIMPayload {
	filtered, active := p.partition(req)
	if !active {
		return fimTranscriptMarker.RenderFIM(req)
	}
	return FIMPayload{Prefix: mustRender(fimTranscriptMarkerTmpl, filtered)}
}

// ============================= cwd-grouped =================================

type cwdGroupedPrompt struct{}

var cwdGrouped = cwdGroupedPrompt{}

var _ interface {
	ChatPrompt
	FIMPrompt
} = cwdGrouped

func (cwdGroupedPrompt) Name() string { return "cwd-grouped" }

func (cwdGroupedPrompt) partition(req protocol.Request) (same, other []protocol.HistoryEntry, active bool) {
	if req.Cwd == "" || !req.HasHistoryCwd() {
		return nil, nil, false
	}
	for _, e := range req.HistoryWithCwd() {
		if e.Cwd == req.Cwd {
			same = append(same, e)
		} else {
			other = append(other, e)
		}
	}
	if len(same) > defaultHistoryN {
		same = same[len(same)-defaultHistoryN:]
	}
	if len(other) > defaultHistoryN {
		other = other[len(other)-defaultHistoryN:]
	}
	return same, other, true
}

func cmdsOf(es []protocol.HistoryEntry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.Cmd
	}
	return out
}

var cwdGroupedChatContextTmpl = mustParse("cwd-grouped/context", `
{{- if .HasContext}}Context:
{{- if .Cwd}}
- cwd: {{.Cwd}}{{end}}
{{- if .DirEntries}}
- files: {{join .DirEntries " "}}{{end}}
{{- if .GitBranch}}
- git: branch {{.GitBranch}}{{if .GitDirty}} (dirty){{end}}{{end}}
{{- if .LastExit}}
- last command failed (exit {{.LastExit}}){{end}}
{{- if .OtherCmds}}
- earlier commands: {{join .OtherCmds "; "}}{{end}}
{{- if .SameCmds}}
- commands run in {{.Cwd}}: {{join .SameCmds "; "}}{{end}}{{print "\n\n"}}{{end -}}`)

func (cwdGroupedPrompt) chatContext(req protocol.Request, same, other []protocol.HistoryEntry) string {
	view := struct {
		protocol.Request
		OtherCmds, SameCmds []string
		HasContext          bool
	}{
		Request:   req,
		OtherCmds: cmdsOf(other),
		SameCmds:  cmdsOf(same),
		HasContext: req.Cwd != "" || len(req.DirEntries) > 0 || req.GitBranch != "" ||
			req.LastExit != 0 || len(same) > 0 || len(other) > 0,
	}
	return mustRender(cwdGroupedChatContextTmpl, view)
}

func (p cwdGroupedPrompt) RenderChat(req protocol.Request) ChatPayload {
	same, other, active := p.partition(req)
	if !active {
		return chatAppend.RenderChat(req)
	}
	view := struct {
		protocol.Request
		Context       string
		IsNextCommand bool
	}{
		Request:       req,
		Context:       p.chatContext(req, same, other),
		IsNextCommand: req.Kind == protocol.KindNextCommand,
	}
	return ChatPayload{System: chatSystemPrompt, User: mustRender(chatUserTmpl, view)}
}

var cwdGroupedFIMTmpl = mustParse("cwd-grouped/fim", `
{{- if .Cwd}}# cwd: {{.Cwd}}
{{end}}
{{- if .DirEntries}}# files: {{join .DirEntries " "}}
{{end}}
{{- if .GitBranch}}# git: branch {{.GitBranch}}{{if .GitDirty}} (dirty){{end}}
{{end}}
{{- if .Other}}# earlier commands:
{{range .Other}}$ {{.Cmd}}
{{end}}
{{- end}}
{{- if .Same}}# commands run in {{.Cwd}}:
{{range .Same}}$ {{.Cmd}}
{{end}}
{{- end}}
{{- if .LastExit}}# last command failed (exit {{.LastExit}})
{{end}}
{{- print "$ "}}{{.Buf}}`)

func (p cwdGroupedPrompt) RenderFIM(req protocol.Request) FIMPayload {
	same, other, active := p.partition(req)
	if !active {
		return fimTranscriptMarker.RenderFIM(req)
	}
	view := struct {
		protocol.Request
		Same, Other []protocol.HistoryEntry
	}{Request: req, Same: same, Other: other}
	return FIMPayload{Prefix: mustRender(cwdGroupedFIMTmpl, view)}
}
