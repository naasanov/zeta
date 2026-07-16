// Package config parses the daemon's TOML configuration (design §6):
// provider profiles (openai/anthropic/codestral, base URL, model, key
// source), plus the top-level debounce/max-tokens/default-profile knobs that
// used to be env-only.
//
// This package is PURE: Parse takes bytes and returns a validated Config, no
// filesystem or env reads. That mirrors internal/metrics and internal/server
// (design "internal/ packages never read env") and keeps Parse trivially
// testable with inline TOML strings. The one deliberate exception is
// Profile.ResolveKey, which the doc comment on that method calls out
// explicitly: resolving a key inherently means touching the environment or
// shelling out, so it's isolated there rather than smeared across Parse.
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

// validProviders is the set of provider names newProvider (cmd/autopilotd)
// knows how to construct. Kept here, not in cmd/autopilotd, so Parse can
// validate at load time instead of failing later inside the switch.
var validProviders = map[string]bool{
	"openai":    true,
	"anthropic": true,
	"codestral": true,
}

// Config is the top-level parsed shape of config.toml.
type Config struct {
	DefaultProfile string             `toml:"default_profile"`
	DebounceMS     int                `toml:"debounce_ms"`
	MaxTokens      int                `toml:"max_tokens"`
	Profiles       map[string]Profile `toml:"profiles"`
}

// Profile is one named provider configuration under [profiles.<name>].
type Profile struct {
	Provider  string `toml:"provider"` // "openai" | "anthropic" | "codestral"
	BaseURL   string `toml:"base_url"` // ignored by the anthropic adapter (no baseURL param)
	Model     string `toml:"model"`
	APIKeyEnv string `toml:"api_key_env"` // read first
	APIKeyCmd string `toml:"api_key_cmd"` // fallback: shell command whose stdout is the key
}

// Parse decodes TOML bytes into a Config, applies defaults for absent
// top-level fields, and validates cross-field invariants that the TOML
// decoder itself can't express (unknown provider names, a default_profile
// that doesn't exist). A malformed document, an unknown provider, or a
// dangling default_profile are all reported as errors here rather than
// deferred to first use, so a bad config.toml fails at startup, not on the
// first keystroke.
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
			names := make([]string, 0, len(validProviders))
			for n := range validProviders {
				names = append(names, n)
			}
			sort.Strings(names)
			return Config{}, fmt.Errorf("config: profile %q: unknown provider %q, want one of %s", name, p.Provider, strings.Join(names, ", "))
		}
	}

	if cfg.DefaultProfile != "" {
		if _, ok := cfg.Profiles[cfg.DefaultProfile]; !ok {
			return Config{}, fmt.Errorf("config: default_profile %q does not name a configured profile", cfg.DefaultProfile)
		}
	}

	return cfg, nil
}

// ResolveKey resolves this profile's API key: APIKeyEnv first, then
// APIKeyCmd as a fallback that shells out. Neither set is valid (a
// local/Ollama-style profile needs no key at all) and returns "", nil.
//
// The command path trims trailing whitespace from stdout because password
// managers (`pass show ...`, `op read ...`) always emit a trailing newline;
// leaving it in place produces a key that's silently wrong — the request
// still goes out, just with an invalid Authorization header, which shows up
// as a baffling 401 instead of a clear config error.
func (p Profile) ResolveKey() (string, error) {
	if p.APIKeyEnv != "" {
		if v := os.Getenv(p.APIKeyEnv); v != "" {
			return v, nil
		}
	}
	if p.APIKeyCmd != "" {
		out, err := exec.Command("sh", "-c", p.APIKeyCmd).Output()
		if err != nil {
			return "", fmt.Errorf("config: api_key_cmd %q: %w", p.APIKeyCmd, err)
		}
		return strings.TrimRight(string(out), " \t\r\n"), nil
	}
	return "", nil
}
