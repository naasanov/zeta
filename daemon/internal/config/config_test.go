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
provider = "groq"

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
				if groq.Provider != "groq" {
					t.Errorf("groq profile = %+v, unexpected fields", groq)
				}
			},
		},
		{
			name: "defaults applied when absent",
			toml: `
default_profile = "local"

[profiles.local]
provider = "ollama"
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
			name: "bare preset-brand profile with no overrides is allowed",
			toml: `
[profiles.local]
provider = "ollama"
`,
			check: func(t *testing.T, cfg Config) {
				p := cfg.Profiles["local"]
				if p.APIKeyEnv != "" || p.APIKeyCmd != "" || p.BaseURL != "" || p.Model != "" {
					t.Errorf("expected no override fields, got %+v", p)
				}
			},
		},
		{
			name: "openai escape hatch profile is allowed through Parse without required-field check",
			toml: `
[profiles.together]
provider = "openai"
`,
			// Parse deliberately does not validate the openai escape hatch's
			// required fields (base_url/model/api_key_env|api_key_cmd) — that
			// check lives in Resolve only.
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
provider = "groq"
`,
			wantErr: true,
		},
		{
			name: "default_profile naming a preset brand is valid without a matching profile",
			toml: `
default_profile = "codestral"
`,
		},
		{
			name: "default_profile naming openai escape hatch is valid without a matching profile",
			toml: `
default_profile = "openai"
`,
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

func TestConfigResolve(t *testing.T) {
	t.Run("bare brand resolves from preset (simple path)", func(t *testing.T) {
		cfg := Config{}
		r, err := cfg.Resolve("codestral")
		if err != nil {
			t.Fatalf("Resolve() err = %v, want nil", err)
		}
		want := ResolvedProfile{
			Provider:  "codestral",
			Adapter:   "codestral",
			BaseURL:   "https://api.mistral.ai",
			Model:     "codestral-latest",
			APIKeyEnv: "ZSH_AUTOPILOT_CODESTRAL_KEY",
		}
		if r != want {
			t.Errorf("Resolve() = %+v, want %+v", r, want)
		}
	})

	t.Run("groq preset uses openai adapter", func(t *testing.T) {
		cfg := Config{}
		r, err := cfg.Resolve("groq")
		if err != nil {
			t.Fatalf("Resolve() err = %v, want nil", err)
		}
		if r.Adapter != "openai" {
			t.Errorf("Adapter = %v, want openai", r.Adapter)
		}
		if r.BaseURL != "https://api.groq.com/openai/v1" {
			t.Errorf("BaseURL = %v, want groq base url", r.BaseURL)
		}
		if r.Model != "llama-3.3-70b-versatile" {
			t.Errorf("Model = %v, want llama-3.3-70b-versatile", r.Model)
		}
		if r.APIKeyEnv != "ZSH_AUTOPILOT_GROQ_KEY" {
			t.Errorf("APIKeyEnv = %v, want ZSH_AUTOPILOT_GROQ_KEY", r.APIKeyEnv)
		}
	})

	t.Run("ollama preset has no key env", func(t *testing.T) {
		cfg := Config{}
		r, err := cfg.Resolve("ollama")
		if err != nil {
			t.Fatalf("Resolve() err = %v, want nil", err)
		}
		if r.APIKeyEnv != "" {
			t.Errorf("APIKeyEnv = %q, want empty (ollama needs no key)", r.APIKeyEnv)
		}
		if r.Adapter != "openai" {
			t.Errorf("Adapter = %v, want openai", r.Adapter)
		}
		if r.Model != "qwen2.5-coder:1.5b" {
			t.Errorf("Model = %v, want qwen2.5-coder:1.5b", r.Model)
		}
	})

	t.Run("profile overrides preset defaults", func(t *testing.T) {
		cfg := Config{
			Profiles: map[string]Profile{
				"quality": {
					Provider:  "anthropic",
					Model:     "claude-opus-4-8",
					APIKeyEnv: "MY_CUSTOM_KEY",
				},
			},
		}
		r, err := cfg.Resolve("quality")
		if err != nil {
			t.Fatalf("Resolve() err = %v, want nil", err)
		}
		if r.Adapter != "anthropic" {
			t.Errorf("Adapter = %v, want anthropic", r.Adapter)
		}
		if r.Model != "claude-opus-4-8" {
			t.Errorf("Model = %v, want override claude-opus-4-8", r.Model)
		}
		if r.APIKeyEnv != "MY_CUSTOM_KEY" {
			t.Errorf("APIKeyEnv = %v, want override MY_CUSTOM_KEY", r.APIKeyEnv)
		}
	})

	t.Run("api_key_cmd carries through a preset brand profile", func(t *testing.T) {
		cfg := Config{
			Profiles: map[string]Profile{
				"secure-groq": {
					Provider:  "groq",
					APIKeyCmd: "pass show groq/api-key",
				},
			},
		}
		r, err := cfg.Resolve("secure-groq")
		if err != nil {
			t.Fatalf("Resolve() err = %v, want nil", err)
		}
		if r.APIKeyCmd != "pass show groq/api-key" {
			t.Errorf("APIKeyCmd = %v, want pass show groq/api-key", r.APIKeyCmd)
		}
	})

	t.Run("unknown name errors", func(t *testing.T) {
		cfg := Config{}
		_, err := cfg.Resolve("openrouter")
		if err == nil {
			t.Fatalf("Resolve() err = nil, want error for unknown name")
		}
	})

	t.Run("openai escape hatch resolves when fully specified", func(t *testing.T) {
		cfg := Config{
			Profiles: map[string]Profile{
				"together": {
					Provider:  "openai",
					BaseURL:   "https://api.together.xyz/v1",
					Model:     "some-model",
					APIKeyEnv: "TOGETHER_API_KEY",
				},
			},
		}
		r, err := cfg.Resolve("together")
		if err != nil {
			t.Fatalf("Resolve() err = %v, want nil", err)
		}
		want := ResolvedProfile{
			Provider:  "openai",
			Adapter:   "openai",
			BaseURL:   "https://api.together.xyz/v1",
			Model:     "some-model",
			APIKeyEnv: "TOGETHER_API_KEY",
		}
		if r != want {
			t.Errorf("Resolve() = %+v, want %+v", r, want)
		}
	})

	t.Run("openai escape hatch requires base_url, model, and a key source", func(t *testing.T) {
		cfg := Config{
			Profiles: map[string]Profile{
				"together": {Provider: "openai"},
			},
		}
		if _, err := cfg.Resolve("together"); err == nil {
			t.Fatalf("Resolve() err = nil, want error for incomplete openai escape hatch")
		}
	})

	t.Run("openai escape hatch bare-brand (no profile) also requires fields", func(t *testing.T) {
		cfg := Config{}
		if _, err := cfg.Resolve("openai"); err == nil {
			t.Fatalf("Resolve() err = nil, want error (openai isn't a preset, bare selection can't satisfy required fields)")
		}
	})

	t.Run("openai escape hatch accepts api_key_cmd in place of api_key_env", func(t *testing.T) {
		cfg := Config{
			Profiles: map[string]Profile{
				"together": {
					Provider:  "openai",
					BaseURL:   "https://api.together.xyz/v1",
					Model:     "some-model",
					APIKeyCmd: "pass show together/api-key",
				},
			},
		}
		if _, err := cfg.Resolve("together"); err != nil {
			t.Fatalf("Resolve() err = %v, want nil", err)
		}
	})
}

func TestResolvedProfileResolveKey(t *testing.T) {
	t.Run("env path", func(t *testing.T) {
		t.Setenv("TEST_AUTOPILOT_KEY", "sk-from-env")
		r := ResolvedProfile{APIKeyEnv: "TEST_AUTOPILOT_KEY"}
		got, err := r.ResolveKey()
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
		r := ResolvedProfile{APIKeyCmd: "echo sk-from-cmd"}
		got, err := r.ResolveKey()
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
		r := ResolvedProfile{}
		got, err := r.ResolveKey()
		if err != nil {
			t.Fatalf("ResolveKey() err = %v, want nil", err)
		}
		if got != "" {
			t.Errorf("ResolveKey() = %v, want empty string", got)
		}
	})

	t.Run("env set takes precedence over cmd", func(t *testing.T) {
		t.Setenv("TEST_AUTOPILOT_KEY2", "sk-from-env")
		r := ResolvedProfile{APIKeyEnv: "TEST_AUTOPILOT_KEY2", APIKeyCmd: "echo sk-from-cmd"}
		got, err := r.ResolveKey()
		if err != nil {
			t.Fatalf("ResolveKey() err = %v, want nil", err)
		}
		if got != "sk-from-env" {
			t.Errorf("ResolveKey() = %v, want sk-from-env (env takes precedence)", got)
		}
	})
}
