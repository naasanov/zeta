package config

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		toml    string
		wantErr bool
		check   func(t *testing.T, cfg Config)
	}{
		{
			name: "valid multi-profile",
			toml: `
default_profile = "groq"
debounce_ms = 150
max_tokens = 64

[profiles.groq]
provider = "openai"
base_url = "https://api.groq.com/openai/v1"
model = "llama-3.3-70b-versatile"
api_key_env = "GROQ_API_KEY"

[profiles.haiku]
provider = "anthropic"
model = "claude-haiku-4-5"
api_key_env = "ANTHROPIC_API_KEY"
`,
			check: func(t *testing.T, cfg Config) {
				if cfg.DefaultProfile != "groq" {
					t.Errorf("DefaultProfile = %v, want groq", cfg.DefaultProfile)
				}
				if cfg.DebounceMS != 150 {
					t.Errorf("DebounceMS = %v, want 150", cfg.DebounceMS)
				}
				if cfg.MaxTokens != 64 {
					t.Errorf("MaxTokens = %v, want 64", cfg.MaxTokens)
				}
				if len(cfg.Profiles) != 2 {
					t.Errorf("len(Profiles) = %v, want 2", len(cfg.Profiles))
				}
				groq, ok := cfg.Profiles["groq"]
				if !ok {
					t.Fatalf("profiles[groq] missing")
				}
				if groq.Provider != "openai" || groq.Model != "llama-3.3-70b-versatile" {
					t.Errorf("groq profile = %+v, unexpected fields", groq)
				}
			},
		},
		{
			name: "defaults applied when absent",
			toml: `
default_profile = "local"

[profiles.local]
provider = "openai"
base_url = "http://localhost:11434/v1"
model = "llama3"
`,
			check: func(t *testing.T, cfg Config) {
				if cfg.DebounceMS != DefaultDebounceMS {
					t.Errorf("DebounceMS = %v, want default %v", cfg.DebounceMS, DefaultDebounceMS)
				}
				if cfg.MaxTokens != DefaultMaxTokens {
					t.Errorf("MaxTokens = %v, want default %v", cfg.MaxTokens, DefaultMaxTokens)
				}
			},
		},
		{
			name: "profile with neither key field is allowed",
			toml: `
[profiles.local]
provider = "openai"
base_url = "http://localhost:11434/v1"
model = "llama3"
`,
			check: func(t *testing.T, cfg Config) {
				p := cfg.Profiles["local"]
				if p.APIKeyEnv != "" || p.APIKeyCmd != "" {
					t.Errorf("expected no key fields, got %+v", p)
				}
			},
		},
		{
			name: "unknown provider rejected",
			toml: `
[profiles.bad]
provider = "openrouter"
model = "whatever"
`,
			wantErr: true,
		},
		{
			name: "default_profile naming nonexistent profile rejected",
			toml: `
default_profile = "missing"

[profiles.groq]
provider = "openai"
model = "llama-3.3-70b-versatile"
`,
			wantErr: true,
		},
		{
			name:    "malformed toml rejected",
			toml:    `this is not [ valid toml`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Parse([]byte(tt.toml))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Parse() err = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse() err = %v, want nil", err)
			}
			if tt.check != nil {
				tt.check(t, cfg)
			}
		})
	}
}

func TestProfileResolveKey(t *testing.T) {
	t.Run("env path", func(t *testing.T) {
		t.Setenv("TEST_AUTOPILOT_KEY", "sk-from-env")
		p := Profile{APIKeyEnv: "TEST_AUTOPILOT_KEY"}
		got, err := p.ResolveKey()
		if err != nil {
			t.Fatalf("ResolveKey() err = %v, want nil", err)
		}
		if got != "sk-from-env" {
			t.Errorf("ResolveKey() = %v, want sk-from-env", got)
		}
	})

	t.Run("cmd path trims trailing newline", func(t *testing.T) {
		// echo always appends a trailing newline; a pass/op-style shell-out
		// behaves the same way, and ResolveKey must trim it or the resolved
		// key is silently wrong (see doc comment on ResolveKey).
		p := Profile{APIKeyCmd: "echo sk-from-cmd"}
		got, err := p.ResolveKey()
		if err != nil {
			t.Fatalf("ResolveKey() err = %v, want nil", err)
		}
		if got != "sk-from-cmd" {
			t.Errorf("ResolveKey() = %q, want %q (no trailing newline)", got, "sk-from-cmd")
		}
		if strings.ContainsAny(got, "\r\n") {
			t.Errorf("ResolveKey() = %q, contains trailing newline", got)
		}
	})

	t.Run("no key fields set returns empty", func(t *testing.T) {
		p := Profile{}
		got, err := p.ResolveKey()
		if err != nil {
			t.Fatalf("ResolveKey() err = %v, want nil", err)
		}
		if got != "" {
			t.Errorf("ResolveKey() = %v, want empty string", got)
		}
	})

	t.Run("env set takes precedence over cmd", func(t *testing.T) {
		t.Setenv("TEST_AUTOPILOT_KEY2", "sk-from-env")
		p := Profile{APIKeyEnv: "TEST_AUTOPILOT_KEY2", APIKeyCmd: "echo sk-from-cmd"}
		got, err := p.ResolveKey()
		if err != nil {
			t.Fatalf("ResolveKey() err = %v, want nil", err)
		}
		if got != "sk-from-env" {
			t.Errorf("ResolveKey() = %v, want sk-from-env (env takes precedence)", got)
		}
	})
}
