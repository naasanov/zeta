// Package protocol defines the wire format between the zsh client and the
// autopilot daemon. It is the single source of truth for the socket contract;
// the zsh side (zsh/45_json.zsh, zsh/50_socket.zsh) mirrors these shapes by
// hand, and the Phase 0 echo-server speaks the same format.
//
// # Framing
//
// Messages are newline-delimited JSON ("JSON Lines"): one compact JSON object
// per line, terminated by '\n'. Because JSON escapes any literal newline in a
// string as "\n", the terminating newline is always unambiguous even when a
// command buffer spans multiple lines. This matches the project's JSONL
// leanings (design §12) and stays language-agnostic for future bash/fish
// clients (§55).
//
// HTML escaping MUST be disabled when encoding (see Encode): Go's default
// json.Marshal rewrites '<', '>', and '&' as \uXXXX, which the lightweight zsh
// decoder does not unescape. Shell commands are full of '>' and '&', so this is
// not optional.
//
// # Request/Reply correlation and supersede semantics (design §11)
//
// Every Request carries an ID minted by the client (per-session, monotonic).
// Every Reply echoes the ID of the Request it answers. The client tracks the
// ID of its most recent Request and paints a Reply only when Reply.ID matches
// that current ID; Replies for older IDs are stale (the user typed on) and are
// dropped. This gives supersede-by-request-ID for free across keystrokes.
//
// A single Request ID may receive more than one Reply. The Source tag
// distinguishes them (e.g. a fast "history" suggestion followed by a slower
// "llm" one that supersedes it — the Phase 4a upgrade-in-place path). Because
// the current-ID check passes for every Reply sharing that ID, a later Reply
// repaints over an earlier one. The daemon only needs to keep emitting Replies
// under the same ID; no protocol change is required when the history engine
// lands. Phase 1 emits exactly one Reply per Request.
//
// # Versioning
//
// Every message carries an integer V. Additive fields are free (both sides
// ignore unknown keys) and do NOT bump V. Bump V only on a semantic break
// (renaming/removing a field, changing a field's meaning or units).
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
// Context fields (design §7, step 5): Cwd, GitBranch, GitDirty, LastExit, and
// History are all OPTIONAL — a request may include any subset, and each
// carries `omitempty` so an absent field is simply missing from the wire
// message rather than sent as a zero value. The daemon folds in whatever is
// present and ignores what's absent (see contextBlock in cmd/autopilotd).
// LastExit specifically omits 0: only a NON-ZERO exit code is meaningful (a
// failed command the model might help fix), so "0" and "absent" both mean
// "nothing to fix" and collapse to the same on-the-wire shape by design.
type Request struct {
	V         int      `json:"v"`
	ID        string   `json:"id"`                   // client-minted, unique within a session
	Kind      string   `json:"kind"`                 // KindTyping | KindNextCommand
	Buf       string   `json:"buf"`                  // the current command-line buffer
	Cwd       string   `json:"cwd,omitempty"`        // absolute current working directory
	GitBranch string   `json:"git_branch,omitempty"` // current git branch; empty/omitted outside a repo
	GitDirty  bool     `json:"git_dirty,omitempty"`  // true if the working tree has uncommitted changes
	LastExit  int      `json:"last_exit,omitempty"`  // exit code of the previous command; 0/absent = nothing to fix
	History   []string `json:"history,omitempty"`    // recent commands, oldest first, newest last
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
