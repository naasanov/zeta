// Package history owns the daemon's command history.
package history

// Entry is one command as it was run. Cwd is "" for entries bootstrapped
// from $HISTFILE, which records a timestamp but no directory. Session
// defines adjacency for the successor-frequency index.
type Entry struct {
	Cmd     string `json:"cmd"`
	Cwd     string `json:"cwd,omitempty"`
	Session string `json:"session,omitempty"`
	Ts      int64  `json:"ts,omitempty"`
}

// Query describes what a retrieval policy should return. N bounds the
// recency portion of the pool, not what a prompt renders.
type Query struct {
	Cwd string
	N   int
}
