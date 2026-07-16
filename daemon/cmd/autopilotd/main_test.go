package main

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTempConfig writes body to a temp config.toml and points
// ZSH_AUTOPILOT_CONFIG at it for the duration of the test.
func writeTempConfig(t *testing.T, body string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	t.Setenv("ZSH_AUTOPILOT_CONFIG", path)
}

// TestLoadConfig_EmptyFileSeedsBuiltinGroq covers the reported bug: an empty
// config.toml must not strand the daemon — the built-in groq profile is seeded
// so ZSH_AUTOPILOT_PROFILE=groq (and the default) still resolve.
func TestLoadConfig_EmptyFileSeedsBuiltinGroq(t *testing.T) {
	writeTempConfig(t, "")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if _, ok := cfg.Profiles["groq"]; !ok {
		t.Errorf("groq profile not seeded into empty config; profiles = %v", cfg.Profiles)
	}
	if cfg.DefaultProfile != "groq" {
		t.Errorf("DefaultProfile = %q, want groq", cfg.DefaultProfile)
	}
}

// TestLoadConfig_SeedsGroqAlongsideUserProfiles checks a config that defines
// only its own profiles still gets the built-in groq available, without
// disturbing the user's default_profile choice.
func TestLoadConfig_SeedsGroqAlongsideUserProfiles(t *testing.T) {
	writeTempConfig(t, `
default_profile = "haiku"
[profiles.haiku]
provider = "anthropic"
model = "claude-haiku-4-5"
api_key_env = "ANTHROPIC_API_KEY"
`)

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if _, ok := cfg.Profiles["haiku"]; !ok {
		t.Errorf("user haiku profile missing; profiles = %v", cfg.Profiles)
	}
	if _, ok := cfg.Profiles["groq"]; !ok {
		t.Errorf("built-in groq not seeded alongside user profiles; profiles = %v", cfg.Profiles)
	}
	if cfg.DefaultProfile != "haiku" {
		t.Errorf("DefaultProfile = %q, want haiku (user choice must survive seeding)", cfg.DefaultProfile)
	}
}

// TestLoadConfig_UserGroqOverridesBuiltin verifies a user-defined profile under
// a built-in name wins — seeding only fills gaps, never clobbers.
func TestLoadConfig_UserGroqOverridesBuiltin(t *testing.T) {
	writeTempConfig(t, `
default_profile = "groq"
[profiles.groq]
provider = "openai"
base_url = "http://localhost:11434/v1"
model = "custom-local"
api_key_env = "GROQ_API_KEY"
`)

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if got := cfg.Profiles["groq"].Model; got != "custom-local" {
		t.Errorf("groq.Model = %q, want custom-local (user entry must win over built-in)", got)
	}
}

// TestLoadConfig_MissingExplicitPathErrors confirms a typo'd ZSH_AUTOPILOT_CONFIG
// fails loudly rather than silently falling back to the built-in default.
func TestLoadConfig_MissingExplicitPathErrors(t *testing.T) {
	t.Setenv("ZSH_AUTOPILOT_CONFIG", filepath.Join(t.TempDir(), "does-not-exist.toml"))

	if _, err := loadConfig(); err == nil {
		t.Error("loadConfig() error = nil, want error for a named-but-missing config path")
	}
}
