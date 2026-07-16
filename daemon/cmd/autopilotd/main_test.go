package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/naasanov/zsh-autopilot/daemon/internal/protocol"
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

// TestLoadConfig_MissingExplicitPathErrors confirms a typo'd ZSH_AUTOPILOT_CONFIG
// fails loudly rather than silently falling back to defaults.
func TestLoadConfig_MissingExplicitPathErrors(t *testing.T) {
	t.Setenv("ZSH_AUTOPILOT_CONFIG", filepath.Join(t.TempDir(), "does-not-exist.toml"))

	if _, err := loadConfig(); err == nil {
		t.Error("loadConfig() error = nil, want error for a named-but-missing config path")
	}
}

// TestLoadConfig_EmptyFileAppliesDefaults confirms an empty config.toml (no
// profiles at all) still loads cleanly with the package defaults — presets
// resolve on demand via Config.Resolve now, so there's no built-in profile
// left to seed.
func TestLoadConfig_EmptyFileAppliesDefaults(t *testing.T) {
	writeTempConfig(t, "")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if cfg.DebounceMS != 100 {
		t.Errorf("DebounceMS = %v, want 100", cfg.DebounceMS)
	}
	if cfg.MaxTokens != 48 {
		t.Errorf("MaxTokens = %v, want 48", cfg.MaxTokens)
	}
	if cfg.DefaultProfile != "" {
		t.Errorf("DefaultProfile = %q, want empty", cfg.DefaultProfile)
	}
}

// TestLoadConfig_ImplicitMissingFileIsFine confirms the implicit XDG default
// path simply being absent (no ZSH_AUTOPILOT_CONFIG set, and presumably no
// ~/.config/autopilot/config.toml) falls back silently rather than erroring.
func TestLoadConfig_ImplicitMissingFileIsFine(t *testing.T) {
	t.Setenv("ZSH_AUTOPILOT_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // empty dir, no autopilot/config.toml in it

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if cfg.DebounceMS != 100 {
		t.Errorf("DebounceMS = %v, want 100", cfg.DebounceMS)
	}
}

func TestEchoMissingKey(t *testing.T) {
	fn := echoMissingKey("ZSH_AUTOPILOT_GROQ_KEY")

	t.Run("empty buffer gets a comment hint", func(t *testing.T) {
		reply, err := fn(context.Background(), protocol.Request{
			V: protocol.Version, ID: "1", Kind: protocol.KindNextCommand, Buf: "",
		})
		if err != nil {
			t.Fatalf("echoMissingKey fn err = %v, want nil", err)
		}
		want := "# autopilot: set ZSH_AUTOPILOT_GROQ_KEY"
		if reply.Suggestion != want {
			t.Errorf("Suggestion = %q, want %q", reply.Suggestion, want)
		}
		if reply.ID != "1" || reply.Source != protocol.SourceLLM {
			t.Errorf("reply = %+v, want ID=1 Source=llm", reply)
		}
	})

	t.Run("non-empty buffer gets no ghost text", func(t *testing.T) {
		reply, err := fn(context.Background(), protocol.Request{
			V: protocol.Version, ID: "2", Kind: protocol.KindTyping, Buf: "git status",
		})
		if err != nil {
			t.Fatalf("echoMissingKey fn err = %v, want nil", err)
		}
		if reply.Suggestion != "git status" {
			t.Errorf("Suggestion = %q, want %q (no suffix appended)", reply.Suggestion, "git status")
		}
	})
}
