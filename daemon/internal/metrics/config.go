package metrics

import (
	"os"
	"os/user"
	"path/filepath"
)

// Kept here, not cmd/autopilotd, so deleting this directory removes metrics wholesale.
const (
	EnvEnable  = "ZSH_AUTOPILOT_METRICS"        // TEMPORARY: default-ON unless "0"/"false"
	EnvLogPath = "ZSH_AUTOPILOT_METRICS_LOG"    // JSONL path; default DefaultLogPath()
	EnvSocket  = "ZSH_AUTOPILOT_METRICS_SOCKET" // metrics socket; default DefaultSocket
	EnvUser    = "ZSH_AUTOPILOT_USER"           // overrides DefaultUser()

	// EnvRawText gates raw-text capture (buffer, suggestion, ambient context)
	// into the "request" event. TEMPORARILY default-ON since redaction isn't
	// built yet; revert to default-OFF before release.
	EnvRawText = "ZSH_AUTOPILOT_METRICS_RAW_TEXT"
)

// Duplicated from cmd/autopilotd's helper so this package stays import-free of main.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// Config is the fully-resolved metrics configuration: no env lookups happen
// past this point, keeping Logger and Serve testable with plain values.
type Config struct {
	LogPath    string
	SocketPath string
	User       string

	RawText bool
}

// ConfigFromEnv resolves the metrics configuration from the environment; the
// bool reports whether metrics are enabled. TEMPORARY: ON unless EnvEnable is
// "0"/"false"; revert to default-OFF before release.
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
func DefaultUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return "unknown"
}

// DefaultLogPath is the XDG state-dir location for the event log:
// $XDG_STATE_HOME/autopilot/events.jsonl, falling back to
// ~/.local/state/autopilot/events.jsonl.
func DefaultLogPath() string {
	stateHome := os.Getenv("XDG_STATE_HOME")
	if stateHome == "" {
		stateHome = filepath.Join(envOr("HOME", "."), ".local", "state")
	}
	return filepath.Join(stateHome, "autopilot", "events.jsonl")
}
