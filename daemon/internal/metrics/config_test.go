package metrics

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestConfigFromEnv_EnabledByDefault pins the TEMPORARY dogfooding
// default-ON; flips to asserting disabled at the Phase-3 metrics strip.
func TestConfigFromEnv_EnabledByDefault(t *testing.T) {
	t.Setenv(EnvEnable, "")
	if _, ok := ConfigFromEnv(); !ok {
		t.Error("ConfigFromEnv() disabled with no env set, want enabled (dogfooding default-ON)")
	}
}

// TestConfigFromEnv_OnlyExplicitZeroDisables: only literal "0"/"false" turns
// metrics off; every other value leaves them on.
func TestConfigFromEnv_OnlyExplicitZeroDisables(t *testing.T) {
	for _, v := range []string{"0", "false"} {
		t.Setenv(EnvEnable, v)
		if _, ok := ConfigFromEnv(); ok {
			t.Errorf("%s=%q enabled metrics, want disabled", EnvEnable, v)
		}
	}
	for _, v := range []string{"", "1", "true", "yes", "on", "2"} {
		t.Setenv(EnvEnable, v)
		if _, ok := ConfigFromEnv(); !ok {
			t.Errorf("%s=%q disabled metrics, want enabled", EnvEnable, v)
		}
	}
}

// TestConfigFromEnv_Defaults checks resolved values when only the gate is set.
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

// TestConfigFromEnv_RawTextEnabledByDefault pins the TEMPORARY dogfooding
// default-ON; flips to asserting false once raw-text reverts to opt-in.
func TestConfigFromEnv_RawTextEnabledByDefault(t *testing.T) {
	t.Setenv(EnvEnable, "1")
	t.Setenv(EnvRawText, "")

	cfg, ok := ConfigFromEnv()
	if !ok {
		t.Fatal("ConfigFromEnv() disabled, want enabled")
	}
	if !cfg.RawText {
		t.Error("cfg.RawText = false with EnvRawText unset, want true (dogfooding default-ON)")
	}
}

// TestConfigFromEnv_RawTextOnlyExplicitZeroDisables: only literal "0"/"false"
// turns raw-text capture off; every other value leaves it on.
func TestConfigFromEnv_RawTextOnlyExplicitZeroDisables(t *testing.T) {
	t.Setenv(EnvEnable, "1")

	for _, v := range []string{"0", "false"} {
		t.Setenv(EnvRawText, v)
		cfg, ok := ConfigFromEnv()
		if !ok {
			t.Fatal("ConfigFromEnv() disabled, want enabled")
		}
		if cfg.RawText {
			t.Errorf("%s=%q gave RawText=true, want false", EnvRawText, v)
		}
	}

	for _, v := range []string{"", "1", "true", "yes", "on", "2"} {
		t.Setenv(EnvRawText, v)
		cfg, ok := ConfigFromEnv()
		if !ok {
			t.Fatal("ConfigFromEnv() disabled, want enabled")
		}
		if !cfg.RawText {
			t.Errorf("%s=%q gave RawText=false, want true", EnvRawText, v)
		}
	}
}

// TestDefaultUser_NeverEmpty: DefaultUser must never return "" (an empty
// user fails silently as an unattributable row).
func TestDefaultUser_NeverEmpty(t *testing.T) {
	if got := DefaultUser(); strings.TrimSpace(got) == "" {
		t.Error("DefaultUser() = empty, want the OS user or \"unknown\"")
	}
}
