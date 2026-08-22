// Package protocol defines the wire format between the zsh client and the
// autopilot daemon: newline-delimited JSON, one compact object per line. The
// zsh side (zsh/45_json.zsh, zsh/50_socket.zsh) mirrors these shapes by hand.
//
// HTML escaping MUST be disabled when encoding (see Encode): Go's default
// json.Marshal rewrites '<', '>', '&' as \uXXXX, which the zsh decoder does
// not unescape, and shell commands are full of '>' and '&'.
//
// Every Request carries a client-minted, per-session-monotonic ID; every
// Reply echoes it. The client paints only the Reply matching its most recent
// Request ID, dropping stale ones — free supersede-by-ID across keystrokes.
// A single Request ID may receive more than one Reply (Source distinguishes
// them, e.g. a fast "history" reply then a slower "llm" one that repaints
// over it); today exactly one Reply is emitted per Request.
//
// Every message carries an integer V. Additive fields are free (both sides
// ignore unknown keys) and do NOT bump V; bump only on a semantic break.
package protocol

import (
	"encoding/json"
	"io"
)

// Version is the current protocol version stamped into every message.
const Version = 2

// Request kinds. KindTyping and KindNextCommand are the two suggestion paths,
// sharing one system prompt daemon-side. KindRecord is client -> daemon and
// fire-and-forget (no Reply): it feeds the daemon's history store from preexec.
const (
	KindTyping      = "typing"       // fetch a completion for a non-empty buffer
	KindNextCommand = "next_command" // predict the next command on an empty prompt
	KindRecord      = "record"       // record a just-executed command; fire-and-forget, no Reply
)

// Reply sources. The tag travels with every suggestion so the client can apply
// source-specific rendering rules later (design §11) without a protocol change.
const (
	SourceLLM     = "llm"
	SourceHistory = "history"
)

// Request is a client -> daemon message: either a suggestion ask (KindTyping /
// KindNextCommand) or a fire-and-forget history record (KindRecord). Most
// fields are optional (`omitempty`); LastExit treats 0/absent alike as
// "nothing to fix". History/HistoryCwds are daemon-filled, not client-sent.
type Request struct {
	V    int    `json:"v"`
	ID   string `json:"id"`   // client-minted, unique within a session
	Kind string `json:"kind"` // KindTyping | KindNextCommand | KindRecord
	Buf  string `json:"buf"`  // the current command-line buffer

	Cwd        string   `json:"cwd,omitempty"`         // absolute current working directory
	GitBranch  string   `json:"git_branch,omitempty"`  // current git branch; empty/omitted outside a repo
	GitDirty   bool     `json:"git_dirty,omitempty"`   // true if the working tree has uncommitted changes
	LastExit   int      `json:"last_exit,omitempty"`   // exit code of the previous command; 0/absent = nothing to fix
	DirEntries []string `json:"dir_entries,omitempty"` // names (files+dirs) in cwd, no paths; client-capped, omitted when empty/over cap

	// HistoryCwds is index-aligned with History: an unknown directory is "" at
	// the same index, never a shortened array. Use HistoryWithCwd/
	// SetHistoryEntries rather than the raw slices to keep that contract in one place.
	History     []string `json:"history,omitempty"`      // recent commands, oldest first, newest last
	HistoryCwds []string `json:"history_cwds,omitempty"` // cwd each entry ran in; "" = unknown; index-aligned with History

	// Cmd and Ts are KindRecord only: the command just executed and the time
	// (EPOCHSECONDS) it executed at.
	Cmd string `json:"cmd,omitempty"`
	Ts  int64  `json:"ts,omitempty"`
}

// HistoryEntry pairs one history command with the directory it ran in ("" if
// unknown). It is the unzipped view of Request.History/HistoryCwds.
type HistoryEntry struct {
	Cmd string
	Cwd string // "" = unknown
}

// HistoryWithCwd unzips Request.History/HistoryCwds into aligned entries,
// tolerating misalignment (short, long, or absent HistoryCwds) rather than
// panicking — any unpaired position is unknown (""). See SetHistoryEntries.
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

// HasHistoryCwd reports whether at least one history entry carries a known
// (non-empty) cwd. Prompts use it with Req.Cwd != "" to decide whether
// cwd-aware rendering is active, or to fall back to baseline passthrough.
func (r Request) HasHistoryCwd() bool {
	for _, cwd := range r.HistoryCwds {
		if cwd != "" {
			return true
		}
	}
	return false
}

// SetHistoryEntries zips es into Request.History/HistoryCwds — the single
// write path for the alignment contract HistoryWithCwd reads. Empty es
// clears both fields to nil rather than encoding empty-but-present slices.
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

// HistorySegment is a run of commands that all executed in the same
// directory (or, for HistoryUnknown, in an unrecorded one). It is the input
// to SetHistory — see HistoryIn/HistoryUnknown.
type HistorySegment struct {
	Cwd  string // "" = unknown
	Cmds []string
}

// HistoryIn builds a HistorySegment of commands that all ran in cwd.
func HistoryIn(cwd string, cmds ...string) HistorySegment {
	return HistorySegment{Cwd: cwd, Cmds: cmds}
}

// HistoryUnknown builds a HistorySegment of commands whose directory is not
// recorded — the shape of entries bootstrapped from $HISTFILE before the
// daemon started tagging cwds.
func HistoryUnknown(cmds ...string) HistorySegment {
	return HistorySegment{Cwd: "", Cmds: cmds}
}

// SetHistory is segment sugar over SetHistoryEntries: it flattens segs, in
// order, into one History/HistoryCwds pair — "ran these in this directory,
// then cd'd and ran these" — including "unknown after known" in one pass.
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
}

// Encode writes v as one newline-terminated JSON line with HTML escaping
// disabled. Use it for every outbound message so the '<', '>', '&' characters
// common in shell commands survive intact (see the package doc). The trailing
// newline is the frame delimiter.
func Encode(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(v) // json.Encoder.Encode already appends '\n'
}

// NewDecoder returns a decoder that reads newline-delimited JSON messages from
// r. json.Decoder consumes one JSON value per Decode call and treats the
// inter-message whitespace (our '\n') as a separator, so it frames the stream
// for us.
func NewDecoder(r io.Reader) *json.Decoder {
	return json.NewDecoder(r)
}
