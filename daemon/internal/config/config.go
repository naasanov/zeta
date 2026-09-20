// Package config parses the daemon's TOML configuration.
package config

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

const (
	DefaultDebounceMS = 100
	DefaultMaxTokens  = 48
)

// MaxTokens 0 means "use the configured/default max_tokens".
type Preset struct {
	Adapter   string
	BaseURL   string
	Model     string
	KeyEnv    string
	MaxTokens int
}

// "openai" is not a preset here; it's the generic escape hatch.
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
	// gpt-oss-20b can't fully silence its reasoning (accepts only
	// low/medium/high, no "none"); MaxTokens compensates.
	"groq": {
		Adapter:   "openai",
		BaseURL:   "https://api.groq.com/openai/v1",
		Model:     "openai/gpt-oss-20b",
		KeyEnv:    "ZSH_AUTOPILOT_GROQ_KEY",
		MaxTokens: 150,
	},
	"qwen": {
		Adapter: "openai",
		BaseURL: "https://api.deepinfra.com/v1/openai",
		Model:   "Qwen/Qwen3-Coder-480B-A35B-Instruct-Turbo",
		KeyEnv:  "ZSH_AUTOPILOT_DEEPINFRA_KEY",
	},
	"ollama": {
		Adapter: "openai",
		BaseURL: "http://localhost:11434/v1",
		Model:   "qwen2.5-coder:1.5b", // untested placeholder; user must have pulled it
		KeyEnv:  "",                   // local, no key expected
	},
}

var validProviders = func() map[string]bool {
	m := map[string]bool{"openai": true}
	for name := range presets {
		m[name] = true
	}
	return m
}()

func sortedProviderNames() []string {
	names := make([]string, 0, len(validProviders))
	for n := range validProviders {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

type Config struct {
	DefaultProfile string             `toml:"default_profile"`
	DebounceMS     int                `toml:"debounce_ms"`
	MaxTokens      int                `toml:"max_tokens"`
	Profiles       map[string]Profile `toml:"profiles"`
}

// Profile is one named provider configuration under [profiles.<name>].
// BaseURL/Model/APIKeyEnv/APIKeyCmd are optional overrides layered on the
// brand's preset.
type Profile struct {
	Provider  string `toml:"provider"` // brand: "anthropic" | "codestral" | "groq" | "ollama" | "openai" | "qwen"
	BaseURL   string `toml:"base_url"` // ignored by the anthropic adapter (no baseURL param)
	Model     string `toml:"model"`
	APIKeyEnv string `toml:"api_key_env"` // read first
	APIKeyCmd string `toml:"api_key_cmd"` // fallback: shell command whose stdout is the key
	MaxTokens int    `toml:"max_tokens"`  // 0 means "use the preset/global default"
}

// ResolvedProfile is a brand's preset defaults with any profile-level
// overrides applied.
type ResolvedProfile struct {
	Provider  string // the brand selected (e.g. "codestral", "groq", "qwen", "openai")
	Adapter   string // the internal adapter to construct: "openai" | "anthropic" | "codestral"
	BaseURL   string
	Model     string
	APIKeyEnv string
	APIKeyCmd string
	// MaxTokens is 0 unless a preset or the profile set an override; 0
	// means the caller's own maxTokens param wins.
	MaxTokens int
}

// Parse decodes TOML bytes into a Config, applying defaults and validating
// unknown-provider/dangling-default_profile invariants.
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

// Load reads path and parses it into a Config. A missing file is not an
// error unless mustExist is true; then it parses an empty file, so
// defaults apply and Profiles is empty.
func Load(path string, mustExist bool) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) && !mustExist {
			return Parse([]byte{})
		}
		return Config{}, fmt.Errorf("config: read %s: %w", path, err)
	}
	return Parse(data)
}

// Resolve turns a selection (a profile name, a preset brand, or "openai")
// into a fully-resolved ResolvedProfile. A name absent from c.Profiles but
// matching a preset (or "openai") synthesizes a bare Profile{Provider: name}.
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
			MaxTokens: preset.MaxTokens,
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
		if profile.MaxTokens != 0 {
			r.MaxTokens = profile.MaxTokens
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
			MaxTokens: profile.MaxTokens,
		}, nil
	}

	return ResolvedProfile{}, fmt.Errorf("config: unknown provider %q, want one of %s", brand, strings.Join(sortedProviderNames(), ", "))
}

// ResolveKey resolves the API key: APIKeyEnv first, then APIKeyCmd as a
// shell-out fallback; neither set returns "", nil. Trailing whitespace is
// trimmed from the command's stdout to avoid a silently-wrong key.
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

func (r ResolvedProfile) NeedsKey() bool { return r.APIKeyEnv != "" || r.APIKeyCmd != "" }
