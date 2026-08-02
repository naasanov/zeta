package metrics

import (
	"os"
	"os/user"
	"path/filepath"
)

// Environment variables this package reads. They live here, not in
// cmd/autopilotd, so that every piece of metrics knowledge is inside the
// removable package (design §12): stripping metrics means deleting this
// directory, not hunting for stray env lookups in main.
const (
	EnvEnable  = "ZSH_AUTOPILOT_METRICS"        // TEMPORARY dogfooding default-ON: enabled unless "0"/"false" (see ConfigFromEnv)
	EnvLogPath = "ZSH_AUTOPILOT_METRICS_LOG"    // JSONL path; default DefaultLogPath()
	EnvSocket  = "ZSH_AUTOPILOT_METRICS_SOCKET" // metrics socket; default DefaultSocket
	EnvUser    = "ZSH_AUTOPILOT_USER"           // overrides DefaultUser()

	// EnvRawText gates raw-text capture (buffer, suggestion, ambient context)
	// into the "request" event. TEMPORARILY DEFAULT-ON, inverting the stronger
	// §12 rule "no command/buffer text by default" — raw command lines can
	// carry secrets and Phase-3 redaction isn't built yet. Revert to
	// default-OFF before real release (grep METRICS(§12)).
	EnvRawText = "ZSH_AUTOPILOT_METRICS_RAW_TEXT"
)

// envOr returns the environment variable named key, or fallback if unset or
// empty. Deliberately duplicates cmd/autopilotd's identical helper so this
// package stays import-free of main and can be deleted wholesale.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// Config is the fully-resolved metrics configuration: no env lookups happen
// past this point, which is what keeps Logger and Serve testable with plain
// values (see New(path, user)).
type Config struct {
	LogPath    string
	SocketPath string
	User       string

	// RawText gates opt-in raw-text capture (see EnvRawText); default false.
	RawText bool
}

// ConfigFromEnv resolves the metrics configuration from the environment. The
// bool reports whether metrics are enabled; when false, Config is zero and
// the caller wires nothing.
//
// TEMPORARY dogfooding default: metrics are ON unless EnvEnable is explicitly
// "0"/"false", inverting design §12 (default OFF). Revert before the Phase-3
// strip — grep METRICS(§12).
func ConfigFromEnv() (Config, bool) {
	if v := os.Getenv(EnvEnable); v == "0" || v == "false" {
		return Config{}, false
	}
	// Same TEMPORARY default-ON polarity as EnvEnable, for EnvRawText.
	rt := os.Getenv(EnvRawText)
	rawText := rt != "0" && rt != "false"
	return Config{
		LogPath:    envOr(EnvLogPath, DefaultLogPath()),
		SocketPath: envOr(EnvSocket, DefaultSocket),
		User:       envOr(EnvUser, DefaultUser()),
		RawText:    rawText,
	}, true
}

// DefaultUser is the OS user, or "unknown" if it can't be determined.
// EnvUser overrides it since OS usernames collide across machines.
func DefaultUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return "unknown"
}

// DefaultLogPath is the XDG state-dir location for the event log:
// $XDG_STATE_HOME/autopilot/events.jsonl, falling back to
// ~/.local/state/autopilot/events.jsonl. New creates parent dirs as needed.
func DefaultLogPath() string {
	stateHome := os.Getenv("XDG_STATE_HOME")
	if stateHome == "" {
		stateHome = filepath.Join(envOr("HOME", "."), ".local", "state")
	}
	return filepath.Join(stateHome, "autopilot", "events.jsonl")
}
