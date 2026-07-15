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
	EnvEnable  = "ZSH_AUTOPILOT_METRICS"        // "1" enables; anything else (incl. unset) disables
	EnvLogPath = "ZSH_AUTOPILOT_METRICS_LOG"    // JSONL path; default DefaultLogPath()
	EnvSocket  = "ZSH_AUTOPILOT_METRICS_SOCKET" // metrics socket; default DefaultSocket
	EnvUser    = "ZSH_AUTOPILOT_USER"           // overrides DefaultUser()
)

// envOr returns the environment variable named key, or fallback if unset or
// empty.
//
// This deliberately duplicates cmd/autopilotd's identical helper rather than
// sharing one. The daemon's non-metrics config (provider base URL, model)
// resolves through main's copy, so if it imported this one instead, deleting
// this package would break core config — and removability is the whole point
// of the package's shape. Same trade as SessionID duplicating server.shortID.
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
}

// ConfigFromEnv resolves the metrics configuration from the environment. The
// bool reports whether metrics are enabled at all: they default OFF (§12), and
// only EnvEnable=="1" turns them on. When it returns false the Config is zero
// and the caller should wire nothing — no log file, no socket, no emit hook.
func ConfigFromEnv() (Config, bool) {
	if os.Getenv(EnvEnable) != "1" {
		return Config{}, false
	}
	return Config{
		LogPath:    envOr(EnvLogPath, DefaultLogPath()),
		SocketPath: envOr(EnvSocket, DefaultSocket),
		User:       envOr(EnvUser, DefaultUser()),
	}, true
}

// DefaultUser resolves the username stamped on every event when EnvUser isn't
// set: the OS user, or "unknown" if it can't be determined (user.Current can
// fail on odd/CGO-less setups, and an event with no user at all is worse than
// one labelled "unknown").
//
// EnvUser exists to override this because these logs get collected from
// several people's machines: OS usernames collide and are often generic
// ("admin"), which makes per-user attribution useless at analysis time.
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
