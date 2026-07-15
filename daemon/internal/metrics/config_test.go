package metrics

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestConfigFromEnv_DisabledByDefault pins design §12's "default off": with no
// env set, metrics must be entirely inert. This is the removability guarantee
// the daemon leans on — no log file, no second socket, no emit hook.
func TestConfigFromEnv_DisabledByDefault(t *testing.T) {
	t.Setenv(EnvEnable, "")
	if _, ok := ConfigFromEnv(); ok {
		t.Error("ConfigFromEnv() enabled with no env set, want disabled")
	}
}

// TestConfigFromEnv_OnlyExactlyOneEnables guards against a sloppy truthiness
// check creeping in: the contract with the zsh side is the literal string "1",
// and nothing else should switch metrics on.
func TestConfigFromEnv_OnlyExactlyOneEnables(t *testing.T) {
	for _, v := range []string{"", "0", "true", "yes", "on", "2"} {
		t.Setenv(EnvEnable, v)
		if _, ok := ConfigFromEnv(); ok {
			t.Errorf("%s=%q enabled metrics, want only \"1\" to enable", EnvEnable, v)
		}
	}
	t.Setenv(EnvEnable, "1")
	if _, ok := ConfigFromEnv(); !ok {
		t.Errorf("%s=\"1\" did not enable metrics", EnvEnable)
	}
}

// TestConfigFromEnv_Defaults checks the resolved values when only the gate is
// set: XDG state path, the short default socket, and a non-empty user.
func TestConfigFromEnv_Defaults(t *testing.T) {
	t.Setenv(EnvEnable, "1")
	t.Setenv(EnvLogPath, "")
	t.Setenv(EnvSocket, "")
	t.Setenv(EnvUser, "")
	t.Setenv("XDG_STATE_HOME", "/tmp/xdg-state")

	cfg, ok := ConfigFromEnv()
	if !ok {
		t.Fatal("ConfigFromEnv() disabled, want enabled")
	}
	if want := filepath.Join("/tmp/xdg-state", "autopilot", "events.jsonl"); cfg.LogPath != want {
		t.Errorf("LogPath = %q, want %q", cfg.LogPath, want)
	}
	if cfg.SocketPath != DefaultSocket {
		t.Errorf("SocketPath = %q, want %q", cfg.SocketPath, DefaultSocket)
	}
	if cfg.User == "" {
		t.Error("User is empty; every event must carry an attributable user")
	}
}

// TestConfigFromEnv_Overrides checks each env override wins over its default.
func TestConfigFromEnv_Overrides(t *testing.T) {
	t.Setenv(EnvEnable, "1")
	t.Setenv(EnvLogPath, "/tmp/custom/ev.jsonl")
	t.Setenv(EnvSocket, "/tmp/custom.sock")
	t.Setenv(EnvUser, "nico")

	cfg, ok := ConfigFromEnv()
	if !ok {
		t.Fatal("ConfigFromEnv() disabled, want enabled")
	}
	if cfg.LogPath != "/tmp/custom/ev.jsonl" {
		t.Errorf("LogPath = %q, want the override", cfg.LogPath)
	}
	if cfg.SocketPath != "/tmp/custom.sock" {
		t.Errorf("SocketPath = %q, want the override", cfg.SocketPath)
	}
	// The override is the whole point of EnvUser: OS usernames collide across
	// the machines these logs get collected from.
	if cfg.User != "nico" {
		t.Errorf("User = %q, want the override %q", cfg.User, "nico")
	}
}

// TestDefaultLogPath_FallsBackToHome covers the no-XDG_STATE_HOME branch.
func TestDefaultLogPath_FallsBackToHome(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "/tmp/fakehome")

	got := DefaultLogPath()
	if want := filepath.Join("/tmp/fakehome", ".local", "state", "autopilot", "events.jsonl"); got != want {
		t.Errorf("DefaultLogPath() = %q, want %q", got, want)
	}
}

// TestDefaultUser_NeverEmpty is the invariant that matters: whatever happens,
// DefaultUser must return something attributable rather than "". An empty user
// fails silently — it yields a well-formed event whose only symptom is
// unattributable rows once the logs are collected.
func TestDefaultUser_NeverEmpty(t *testing.T) {
	if got := DefaultUser(); strings.TrimSpace(got) == "" {
		t.Error("DefaultUser() = empty, want the OS user or \"unknown\"")
	}
}
