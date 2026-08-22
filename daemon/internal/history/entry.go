// Package history owns the daemon's command history: an in-memory corpus fed
// by `record` messages, persisted to a JSONL journal, and bootstrapped once
// from $HISTFILE. A leaf (stdlib only, no internal/protocol) that reads no
// environment — every path/bound is a parameter to New, from cmd/autopilotd.
package history

// Entry is one command as it was run. Cwd is "" when the directory is
// unknown, the permanent state of everything bootstrapped from $HISTFILE —
// zsh's history format records a timestamp but no directory. Session defines
// adjacency for the planned successor-frequency index.
type Entry struct {
	Cmd     string `json:"cmd"`
	Cwd     string `json:"cwd,omitempty"`
	Session string `json:"session,omitempty"`
	Ts      int64  `json:"ts,omitempty"`
}

// Query describes what a retrieval policy should return. A struct so a new
// policy can key on a new field without touching a call site. N bounds the
// recency portion of the pool, not what a prompt renders.
type Query struct {
	Cwd string
	N   int
}
