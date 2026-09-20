// Package protocol defines the wire format between the zsh client and the daemon.
package protocol

import (
	"encoding/json"
	"io"
)

const Version = 2

const (
	KindTyping      = "typing"       // fetch a completion for a non-empty buffer
	KindNextCommand = "next_command" // predict the next command on an empty prompt
	KindRecord      = "record"       // record a just-executed command; fire-and-forget, no Reply
)

// Reply sources: the tag lets the client apply source-specific rendering.
const (
	SourceLLM     = "llm"
	SourceHistory = "history"
)

// History/HistoryCwds are daemon-filled, not client-sent.
type Request struct {
	V    int    `json:"v"`
	ID   string `json:"id"`   // client-minted, unique within a session
	Kind string `json:"kind"` // KindTyping | KindNextCommand | KindRecord
	Buf  string `json:"buf"`

	Cwd        string   `json:"cwd,omitempty"`        // absolute path
	GitBranch  string   `json:"git_branch,omitempty"` // empty/omitted outside a repo
	GitDirty   bool     `json:"git_dirty,omitempty"`
	LastExit   int      `json:"last_exit,omitempty"`   // 0/absent = nothing to fix
	DirEntries []string `json:"dir_entries,omitempty"` // names (files+dirs) in cwd, no paths; client-capped, omitted when empty/over cap

	// HistoryCwds is index-aligned with History: an unknown directory is ""
	// at the same index, never a shortened array.
	History     []string `json:"history,omitempty"`      // recent commands, oldest first, newest last
	HistoryCwds []string `json:"history_cwds,omitempty"` // cwd each entry ran in; "" = unknown

	// Cmd and Ts are KindRecord only: the command just executed and the time
	// (EPOCHSECONDS) it executed at.
	Cmd string `json:"cmd,omitempty"`
	Ts  int64  `json:"ts,omitempty"`
}

// HistoryEntry pairs one history command with the directory it ran in ("" if unknown).
type HistoryEntry struct {
	Cmd string
	Cwd string // "" = unknown
}

// HistoryWithCwd unzips Request.History/HistoryCwds into aligned entries,
// tolerating misalignment (short, long, or absent HistoryCwds): an unpaired
// position is unknown ("") rather than a panic.
func (r Request) HistoryWithCwd() []HistoryEntry {
	entries := make([]HistoryEntry, len(r.History))
	for i, cmd := range r.History {
		cwd := ""
		if i < len(r.HistoryCwds) {
			cwd = r.HistoryCwds[i]
		}
		entries[i] = HistoryEntry{Cmd: cmd, Cwd: cwd}
	}
	return entries
}

func (r Request) HasHistoryCwd() bool {
	for _, cwd := range r.HistoryCwds {
		if cwd != "" {
			return true
		}
	}
	return false
}

// Empty es clears both fields to nil rather than encoding empty-but-present slices.
func (r *Request) SetHistoryEntries(es []HistoryEntry) {
	if len(es) == 0 {
		r.History = nil
		r.HistoryCwds = nil
		return
	}
	history := make([]string, len(es))
	cwds := make([]string, len(es))
	for i, e := range es {
		history[i] = e.Cmd
		cwds[i] = e.Cwd
	}
	r.History = history
	r.HistoryCwds = cwds
}

// HistorySegment is a run of commands that all executed in the same directory.
type HistorySegment struct {
	Cwd  string // "" = unknown
	Cmds []string
}

func HistoryIn(cwd string, cmds ...string) HistorySegment {
	return HistorySegment{Cwd: cwd, Cmds: cmds}
}

func HistoryUnknown(cmds ...string) HistorySegment {
	return HistorySegment{Cwd: "", Cmds: cmds}
}

// SetHistory flattens segs, in order, into one History/HistoryCwds pair:
// "ran these in this directory, then cd'd and ran these".
func (r *Request) SetHistory(segs ...HistorySegment) {
	var es []HistoryEntry
	for _, seg := range segs {
		for _, cmd := range seg.Cmds {
			es = append(es, HistoryEntry{Cmd: cmd, Cwd: seg.Cwd})
		}
	}
	r.SetHistoryEntries(es)
}

// Reply is a daemon -> client message carrying a suggestion for a Request.
type Reply struct {
	V          int    `json:"v"`
	ID         string `json:"id"`         // echoes the Request.ID being answered
	Source     string `json:"source"`     // SourceLLM | SourceHistory
	Suggestion string `json:"suggestion"` // single line; the client paints the remainder past the buffer

	// Notice/NoticeKind carry a non-recoverable failure the client should
	// surface once per shell.
	Notice     string `json:"notice,omitempty"`      // single-line, human-readable
	NoticeKind string `json:"notice_kind,omitempty"` // dedupe key: auth | bad_request | no_key | provider_init
}

// Encode writes v as one newline-terminated JSON line with HTML escaping
// disabled, so the '<', '>', '&' characters common in shell commands survive
// intact. The trailing newline is the frame delimiter.
func Encode(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(v) // json.Encoder.Encode already appends '\n'
}

// NewDecoder reads newline-delimited JSON messages from r: json.Decoder
// consumes one value per Decode call and treats the inter-message '\n' as a
// separator, so it frames the stream for us.
func NewDecoder(r io.Reader) *json.Decoder {
	return json.NewDecoder(r)
}
