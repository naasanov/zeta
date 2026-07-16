// Command autopilotd is the zsh-autopilot daemon: a long-running process that
// the zsh client talks to over a Unix domain socket. It holds warm keep-alive
// connections to LLM providers, debounces keystroke-driven requests, cancels
// stale in-flight requests, and streams suggestions back to the shell.
//
// See .docs/zeta_design_doc_v3.md §3 for the architecture. Phase 1 step 2
// built only the socket listener and process skeleton, answering every
// request with a canned echo suggestion (internal/server). Step 4 wired in a
// real LLM-backed suggester (internal/provider) when an API key is present.
// Phase 2 (design §6) replaces the single hardcoded Groq client with TOML
// config/profiles (internal/config) selecting among multiple provider
// adapters; this file is still the composition root ONLY — config
// resolution and provider construction happen here, internal/ packages take
// plain resolved values and never read env themselves.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/naasanov/zsh-autopilot/daemon/internal/config"
	"github.com/naasanov/zsh-autopilot/daemon/internal/logging"
	"github.com/naasanov/zsh-autopilot/daemon/internal/metrics"
	"github.com/naasanov/zsh-autopilot/daemon/internal/provider"
	"github.com/naasanov/zsh-autopilot/daemon/internal/server"
	"github.com/naasanov/zsh-autopilot/daemon/internal/suggest"
)

// builtinDefaultConfig reproduces today's (pre-config.toml) behavior exactly:
// a single "groq" profile pointed at the same base URL/model/key-env this
// package used to hardcode. Used whenever config.toml is absent, so a user
// who never creates one sees no change in behavior.
func builtinDefaultConfig() config.Config {
	return config.Config{
		DefaultProfile: "groq",
		DebounceMS:     config.DefaultDebounceMS,
		MaxTokens:      config.DefaultMaxTokens,
		Profiles: map[string]config.Profile{
			"groq": {
				Provider:  "openai",
				BaseURL:   "https://api.groq.com/openai/v1",
				Model:     "llama-3.3-70b-versatile",
				APIKeyEnv: "GROQ_API_KEY",
			},
		},
	}
}

func main() {
	socket := flag.String("socket", server.DefaultSocket, "unix socket path to listen on")
	verbose := flag.Bool("v", false, "enable debug logging")
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	// compactHandler keeps the dev log panel terse: "HH:MM:SS.mmm L msg key=val"
	// instead of slog's verbose time=/level=/msg= prefix (see internal/logging).
	log := slog.New(logging.NewCompactHandler(os.Stderr, level))

	cfg, err := loadConfig()
	if err != nil {
		log.Error("config: failed to load, exiting", "err", err)
		os.Exit(1)
	}

	srv := server.New(*socket, log)
	srv.Debounce = debounceFromEnv(log, cfg)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// METRICS(§12): dev-only JSONL event log, dogfooding-only and stripped in
	// Phase 3 (see internal/metrics). Fully independent of the provider gate
	// below — metrics can run even in echo mode, but the "request" event
	// only exists on the LLM path since only suggest.LLM has anything to
	// report.
	var emit func(metrics.RequestEvent)
	if mcfg, ok := metrics.ConfigFromEnv(); ok {
		mlog, err := metrics.New(mcfg.LogPath, mcfg.User)
		if err != nil {
			log.Error("metrics: failed to open log, metrics disabled", "path", mcfg.LogPath, "err", err)
		} else {
			defer mlog.Close()
			go func() {
				if err := metrics.Serve(ctx, mcfg.SocketPath, mlog, log); err != nil {
					log.Error("metrics: serve exited", "err", err)
				}
			}()
			emit = mlog.EmitRequest
			log.Info("metrics enabled", "log", mcfg.LogPath, "socket", mcfg.SocketPath, "user", mcfg.User)
		}
	}

	profileName := envOr("ZSH_AUTOPILOT_PROFILE", cfg.DefaultProfile)
	profile, ok := cfg.Profiles[profileName]
	if !ok {
		log.Error("config: selected profile does not exist", "profile", profileName)
		os.Exit(1)
	}

	// Preserve existing env overrides on top of the selected profile: these
	// predate config.toml and let a user override base URL/model/debounce
	// without editing the file (e.g. one-off testing against a different
	// endpoint).
	profile.BaseURL = envOr("ZSH_AUTOPILOT_BASE_URL", profile.BaseURL)
	profile.Model = envOr("ZSH_AUTOPILOT_MODEL", profile.Model)

	maxTokens := cfg.MaxTokens

	// Keys never travel via flags/argv (they'd show up in `ps`); env (or
	// api_key_cmd, itself env/exec-based) only.
	apiKey, err := profile.ResolveKey()
	if err != nil {
		log.Error("config: failed to resolve api key, falling back to echo mode", "profile", profileName, "err", err)
		apiKey = ""
	}

	if apiKey != "" {
		p, err := newProvider(profile, apiKey, maxTokens)
		if err != nil {
			log.Error("provider: failed to construct, falling back to echo mode", "profile", profileName, "provider", profile.Provider, "err", err)
		} else {
			srv.SetSuggest(suggest.LLM(p, log, emit))
			// Never log the key itself.
			log.Info("llm mode", "profile", profileName, "provider", profile.Provider, "model", profile.Model)
		}
	} else {
		log.Info("echo mode: no api key resolved for profile", "profile", profileName)
	}

	if err := srv.Run(ctx); err != nil {
		log.Error("daemon exited", "err", err)
		os.Exit(1)
	}
}

// newProvider constructs a provider.Provider from a resolved profile. It
// lives here, not in internal/provider, so that provider adapters never
// import internal/config (config knows about providers; providers must not
// know about config).
func newProvider(p config.Profile, apiKey string, maxTokens int) (provider.Provider, error) {
	switch p.Provider {
	case "openai":
		return provider.NewOpenAI(p.BaseURL, p.Model, apiKey, maxTokens)
	case "anthropic":
		// Anthropic's constructor takes no baseURL; p.BaseURL (if set) is
		// ignored for this provider.
		return provider.NewAnthropic(p.Model, apiKey, maxTokens)
	case "codestral":
		return provider.NewCodestral(p.BaseURL, p.Model, apiKey, maxTokens)
	default:
		// config.Parse already validates this, so reaching here means a
		// built-in default (or hand-built Config) has an unrecognized
		// provider — a programmer error, not user input.
		return nil, fmt.Errorf("main: unknown provider %q", p.Provider)
	}
}

// loadConfig loads $XDG_CONFIG_HOME/autopilot/config.toml (falling back to
// ~/.config/autopilot/config.toml) if present, or the built-in default
// Config reproducing pre-config.toml behavior if not. A missing file is not
// an error; a present-but-malformed one is.
func loadConfig() (config.Config, error) {
	path := configPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return builtinDefaultConfig(), nil
		}
		return config.Config{}, fmt.Errorf("read %s: %w", path, err)
	}
	return config.Parse(data)
}

// configPath resolves the config.toml location: $XDG_CONFIG_HOME/autopilot,
// falling back to ~/.config/autopilot per the XDG base directory spec.
func configPath() string {
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(envOr("HOME", "."), ".config")
	}
	return filepath.Join(configHome, "autopilot", "config.toml")
}

// envOr returns the environment variable named key, or fallback if unset or
// empty.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// debounceFromEnv reads ZSH_AUTOPILOT_DEBOUNCE_MS (design §4: ~80-120ms), the
// quiet period the coordinator waits before dispatching a buffered request to
// the provider. Falls back to cfg.DebounceMS (itself defaulted by
// config.Parse) if unset, empty, or not a valid non-negative integer.
func debounceFromEnv(log *slog.Logger, cfg config.Config) time.Duration {
	v := os.Getenv("ZSH_AUTOPILOT_DEBOUNCE_MS")
	if v == "" {
		return time.Duration(cfg.DebounceMS) * time.Millisecond
	}
	ms, err := strconv.Atoi(v)
	if err != nil || ms < 0 {
		log.Error("invalid ZSH_AUTOPILOT_DEBOUNCE_MS, using config default", "value", v, "default_ms", cfg.DebounceMS)
		return time.Duration(cfg.DebounceMS) * time.Millisecond
	}
	return time.Duration(ms) * time.Millisecond
}
