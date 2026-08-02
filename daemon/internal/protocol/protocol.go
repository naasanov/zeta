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
const Version = 1

// Request kinds. Both are active: the client fetches a completion as the user
// types (KindTyping) and predicts a next command on the empty prompt from the
// precmd hook (KindNextCommand). They share one system prompt on the daemon
// side — next-command is just the append contract with an empty buffer.
const (
	KindTyping      = "typing"       // fetch a completion for a non-empty buffer
	KindNextCommand = "next_command" // predict the next command on an empty prompt
)

// Reply sources. The tag travels with every suggestion so the client can apply
// source-specific rendering rules later (design §11) without a protocol change.
const (
	SourceLLM     = "llm"
	SourceHistory = "history"
)

// Request is a client -> daemon message asking for a suggestion.
//
// Cwd, GitBranch, GitDirty, LastExit, History, and DirEntries are all
// optional (`omitempty`); LastExit specifically treats 0 and absent as the
// same "nothing to fix" state, since only a non-zero exit is meaningful.
type Request struct {
	V          int      `json:"v"`
	ID         string   `json:"id"`                    // client-minted, unique within a session
	Kind       string   `json:"kind"`                  // KindTyping | KindNextCommand
	Buf        string   `json:"buf"`                   // the current command-line buffer
	Cwd        string   `json:"cwd,omitempty"`         // absolute current working directory
	GitBranch  string   `json:"git_branch,omitempty"`  // current git branch; empty/omitted outside a repo
	GitDirty   bool     `json:"git_dirty,omitempty"`   // true if the working tree has uncommitted changes
	LastExit   int      `json:"last_exit,omitempty"`   // exit code of the previous command; 0/absent = nothing to fix
	History    []string `json:"history,omitempty"`     // recent commands, oldest first, newest last
	DirEntries []string `json:"dir_entries,omitempty"` // names (files+dirs) in cwd, no paths; client-capped, omitted when empty/over cap
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
