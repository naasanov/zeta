// Package config parses the daemon's TOML configuration (design §6):
// provider profiles, plus the top-level debounce/max-tokens/default-profile
// knobs. `provider` is a user-facing BRAND (codestral/anthropic/groq/ollama)
// backed by a preset with tested base_url/model/api_key_env defaults; `openai`
// is a generic escape hatch, NOT a preset — it requires an explicit
// [profiles.*] block since there's nothing sensible to default it to.
//
// This package is PURE: Parse takes bytes and returns a validated Config, no
// filesystem or env reads. The one exception is ResolvedProfile.ResolveKey,
// which shells out/reads env to resolve a key.
package config

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// Default values applied by Parse when the corresponding TOML key is absent
// (zero value). These reproduce today's env-only defaults (design §4/§5)
// so an empty or partial config.toml doesn't change existing behavior.
const (
	DefaultDebounceMS = 100
	DefaultMaxTokens  = 48
)

// Preset is a brand's tested defaults: which internal adapter it speaks,
// where it lives, which model to use, and which env var carries its key.
type Preset struct {
	Adapter string
	BaseURL string
	Model   string
	KeyEnv  string
}

// presets is the brand table (T2.5). "openai" is deliberately absent — it's
// the generic escape hatch, not a preset, and is handled specially in
// Resolve.
//
// qwen2.5-coder:1.5b (ollama) is an UNTESTED placeholder default for local
// use — the user must have `ollama pull`ed it themselves. This is unlike the
// codestral/anthropic/groq picks, which are dogfood-tested defaults. Override
// with `model` in a profile, or ZSH_AUTOPILOT_MODEL.
var presets = map[string]Preset{
	"codestral": {
		Adapter: "codestral",
		BaseURL: "https://api.mistral.ai",
		Model:   "codestral-latest",
		KeyEnv:  "ZSH_AUTOPILOT_CODESTRAL_KEY",
	},
	"anthropic": {
		Adapter: "anthropic",
		BaseURL: "", // native adapter takes no base URL
		Model:   "claude-haiku-4-5",
		KeyEnv:  "ZSH_AUTOPILOT_ANTHROPIC_KEY",
	},
	"groq": {
		Adapter: "openai",
		BaseURL: "https://api.groq.com/openai/v1",
		Model:   "llama-3.3-70b-versatile",
		KeyEnv:  "ZSH_AUTOPILOT_GROQ_KEY",
	},
	"ollama": {
		Adapter: "openai",
		BaseURL: "http://localhost:11434/v1",
		Model:   "qwen2.5-coder:1.5b", // untested placeholder; user must have pulled it
		KeyEnv:  "",                   // local, no key expected
	},
}

// validProviders is the set of provider (brand) names Parse accepts in a
// profile: every preset brand, plus "openai" (the escape hatch).
var validProviders = func() map[string]bool {
	m := map[string]bool{"openai": true}
	for name := range presets {
		m[name] = true
	}
	return m
}()

// sortedProviderNames returns the valid provider/brand names, sorted, for use
// in error messages.
func sortedProviderNames() []string {
	names := make([]string, 0, len(validProviders))
	for n := range validProviders {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Config is the top-level parsed shape of config.toml.
type Config struct {
	DefaultProfile string             `toml:"default_profile"`
	DebounceMS     int                `toml:"debounce_ms"`
	MaxTokens      int                `toml:"max_tokens"`
	Profiles       map[string]Profile `toml:"profiles"`
}

// Profile is one named provider configuration under [profiles.<name>].
// Provider is a brand (a preset name, or "openai" for the escape hatch);
// BaseURL/Model/APIKeyEnv/APIKeyCmd are OPTIONAL overrides layered on top of
// the brand's preset (see Resolve) — for a preset brand, a profile need only
// set Provider and everything else defaults.
type Profile struct {
	Provider  string `toml:"provider"`    // brand: "anthropic" | "codestral" | "groq" | "ollama" | "openai"
	BaseURL   string `toml:"base_url"`    // override; ignored by the anthropic adapter (no baseURL param)
	Model     string `toml:"model"`       // override
	APIKeyEnv string `toml:"api_key_env"` // override; read first
	APIKeyCmd string `toml:"api_key_cmd"` // override; fallback: shell command whose stdout is the key
}

// ResolvedProfile is the fully-resolved result of Config.Resolve: a brand's
// preset defaults with any profile-level overrides applied, ready to
// construct a provider.Provider from (see cmd/autopilotd's newProvider).
type ResolvedProfile struct {
	Provider  string // the brand selected (e.g. "codestral", "groq", "openai")
	Adapter   string // the internal adapter to construct: "openai" | "anthropic" | "codestral"
	BaseURL   string
	Model     string
	APIKeyEnv string
	APIKeyCmd string
}

// Parse decodes TOML bytes into a Config, applies defaults for absent
// top-level fields, and validates cross-field invariants the TOML decoder
// can't express (unknown provider/brand, dangling default_profile) so a bad
// config.toml fails at startup, not on the first keystroke.
//
// Parse deliberately does NOT validate the openai escape hatch's required
// fields — that lives in Resolve only, since Parse can't tell a bare-brand
// profile (valid) from an incomplete escape-hatch one.
func Parse(data []byte) (Config, error) {
	var cfg Config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("config: parse toml: %w", err)
	}

	if cfg.DebounceMS == 0 {
		cfg.DebounceMS = DefaultDebounceMS
	}
	if cfg.MaxTokens == 0 {
		cfg.MaxTokens = DefaultMaxTokens
	}

	for name, p := range cfg.Profiles {
		if !validProviders[p.Provider] {
			return Config{}, fmt.Errorf("config: profile %q: unknown provider %q, want one of %s", name, p.Provider, strings.Join(sortedProviderNames(), ", "))
		}
	}

	if cfg.DefaultProfile != "" {
		_, isProfile := cfg.Profiles[cfg.DefaultProfile]
		_, isPreset := presets[cfg.DefaultProfile]
		isOpenAI := cfg.DefaultProfile == "openai"
		if !isProfile && !isPreset && !isOpenAI {
			return Config{}, fmt.Errorf("config: default_profile %q does not name a configured profile or a known provider", cfg.DefaultProfile)
		}
	}

	return cfg, nil
}

// Resolve turns a selection (a config-profile name, a preset brand, or the
// "openai" escape hatch) into a fully-resolved ResolvedProfile ready for
// provider construction: a name not in c.Profiles but matching a preset brand
// (or "openai") synthesizes a bare Profile{Provider: name} — the simple path,
// e.g. ZSH_AUTOPILOT_PROVIDER=codestral with no [profiles.*] block. Preset
// brands fill Adapter/BaseURL/Model/APIKeyEnv from the preset, then let the
// profile override; "openai" requires base_url, model, and
// api_key_env|api_key_cmd straight from the profile (so it's only reachable
// via a [profiles.*] block, never the bare env path).
func (c Config) Resolve(name string) (ResolvedProfile, error) {
	profile, ok := c.Profiles[name]
	if !ok {
		_, isPreset := presets[name]
		if name == "openai" || isPreset {
			profile = Profile{Provider: name}
		} else {
			return ResolvedProfile{}, fmt.Errorf("config: %q is not a known provider or profile (providers: %s)", name, strings.Join(sortedProviderNames(), ", "))
		}
	}

	brand := profile.Provider

	if preset, ok := presets[brand]; ok {
		r := ResolvedProfile{
			Provider:  brand,
			Adapter:   preset.Adapter,
			BaseURL:   preset.BaseURL,
			Model:     preset.Model,
			APIKeyEnv: preset.KeyEnv,
			APIKeyCmd: profile.APIKeyCmd,
		}
		if profile.BaseURL != "" {
			r.BaseURL = profile.BaseURL
		}
		if profile.Model != "" {
			r.Model = profile.Model
		}
		if profile.APIKeyEnv != "" {
			r.APIKeyEnv = profile.APIKeyEnv
		}
		return r, nil
	}

	if brand == "openai" {
		var missing []string
		if profile.BaseURL == "" {
			missing = append(missing, "base_url")
		}
		if profile.Model == "" {
			missing = append(missing, "model")
		}
		if profile.APIKeyEnv == "" && profile.APIKeyCmd == "" {
			missing = append(missing, "api_key_env (or api_key_cmd)")
		}
		if len(missing) > 0 {
			return ResolvedProfile{}, fmt.Errorf(`config: provider "openai" requires base_url, model, and api_key_env (or api_key_cmd)`)
		}
		return ResolvedProfile{
			Provider:  brand,
			Adapter:   "openai",
			BaseURL:   profile.BaseURL,
			Model:     profile.Model,
			APIKeyEnv: profile.APIKeyEnv,
			APIKeyCmd: profile.APIKeyCmd,
		}, nil
	}

	// Parse already validates profile.Provider against validProviders, so
	// reaching here means a hand-built Config (not run through Parse) named
	// an unrecognized brand — a programmer error, not user input.
	return ResolvedProfile{}, fmt.Errorf("config: unknown provider %q, want one of %s", brand, strings.Join(sortedProviderNames(), ", "))
}

// ResolveKey resolves this profile's API key: APIKeyEnv first, then
// APIKeyCmd as a shell-out fallback. Neither set (e.g. a local/Ollama
// profile) returns "", nil.
//
// Trailing whitespace is trimmed from the command's stdout: password
// managers (`pass show`, `op read`) emit a trailing newline that, left in,
// produces a silently-wrong key — a 401 instead of a clear config error.
func (r ResolvedProfile) ResolveKey() (string, error) {
	if r.APIKeyEnv != "" {
		if v := os.Getenv(r.APIKeyEnv); v != "" {
			return v, nil
		}
	}
	if r.APIKeyCmd != "" {
		out, err := exec.Command("sh", "-c", r.APIKeyCmd).Output()
		if err != nil {
			return "", fmt.Errorf("config: api_key_cmd %q: %w", r.APIKeyCmd, err)
		}
		return strings.TrimRight(string(out), " \t\r\n"), nil
	}
	return "", nil
}

// NeedsKey reports whether this profile requires an API key.
func (r ResolvedProfile) NeedsKey() bool { return r.APIKeyEnv != "" || r.APIKeyCmd != "" }
