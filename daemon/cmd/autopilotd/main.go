// Command autopilotd is the zsh-autopilot daemon: a long-running process that
// the zsh client talks to over a Unix domain socket. It holds warm keep-alive
// connections to LLM providers, debounces keystroke-driven requests, cancels
// stale in-flight requests, and streams suggestions back to the shell.
//
// See .docs/zeta_design_doc_v3.md §3 for the architecture. This file is the
// composition root ONLY — config resolution and provider construction happen
// here; internal/ packages take plain resolved values and never read env.
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
	"github.com/naasanov/zsh-autopilot/daemon/internal/prompt"
	"github.com/naasanov/zsh-autopilot/daemon/internal/protocol"
	"github.com/naasanov/zsh-autopilot/daemon/internal/provider"
	"github.com/naasanov/zsh-autopilot/daemon/internal/server"
	"github.com/naasanov/zsh-autopilot/daemon/internal/suggest"
)

// knownProviders lists the brand names accepted by ZSH_AUTOPILOT_PROVIDER /
// default_profile, for the "you must pick one" error message.
const knownProviders = "anthropic, codestral, groq, ollama, openai"

func main() {
	// The listen socket defaults to $ZSH_AUTOPILOT_SOCKET (the same var the zsh
	// client reads, so the two agree from one place — e.g. the sandbox's .env),
	// falling back to the built-in default; an explicit -socket flag still wins.
	socket := flag.String("socket", envOr("ZSH_AUTOPILOT_SOCKET", server.DefaultSocket), "unix socket path to listen on (default $ZSH_AUTOPILOT_SOCKET)")
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
	// METRICS(§12): rawText is passed into suggest.LLM below; false unless
	// metrics are enabled AND ZSH_AUTOPILOT_METRICS_RAW_TEXT is also set.
	rawText := false
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
			rawText = mcfg.RawText
			if rawText {
				// METRICS(§12): default-ON for dogfooding, so this fires on
				// almost every start — deliberately, so users always know.
				log.Warn("raw-text metrics capture ENABLED (default) — command buffers, suggestions and cwd are written verbatim to the event log; set "+metrics.EnvRawText+"=0 to disable", "log", mcfg.LogPath)
			}
		}
	}

	selected := envOr("ZSH_AUTOPILOT_PROVIDER", cfg.DefaultProfile)
	if selected == "" {
		log.Error("config: no provider selected; set ZSH_AUTOPILOT_PROVIDER or default_profile in config.toml", "providers", knownProviders)
		os.Exit(1)
	}

	resolved, err := cfg.Resolve(selected)
	if err != nil {
		log.Error("config: failed to resolve provider", "selected", selected, "err", err)
		os.Exit(1)
	}

	// Preserve existing env overrides on top of the resolved profile: these
	// predate config.toml and let a user override base URL/model/debounce
	// without editing the file (e.g. one-off testing against a different
	// endpoint).
	resolved.BaseURL = envOr("ZSH_AUTOPILOT_BASE_URL", resolved.BaseURL)
	resolved.Model = envOr("ZSH_AUTOPILOT_MODEL", resolved.Model)

	maxTokens := cfg.MaxTokens

	// Keys never travel via flags/argv (they'd show up in `ps`); env (or
	// api_key_cmd, itself env/exec-based) only.
	apiKey, err := resolved.ResolveKey()
	if err != nil {
		log.Error("config: failed to resolve api key, falling back to echo mode", "provider", selected, "err", err)
		apiKey = ""
	}
	needsKey := resolved.NeedsKey()

	switch {
	case needsKey && apiKey == "":
		// Missing-key echo mode: install a suggest closure that carries the
		// missing key var name so the cause is unmistakable both in the
		// grey ghost-text (a harmless shell comment) and in the daemon log.
		srv.SetSuggest(echoMissingKey(resolved.APIKeyEnv))
		log.Error("echo mode: API key not set", "key_env", resolved.APIKeyEnv)
	default:
		// Either a key was resolved, or this provider needs none (e.g.
		// ollama running locally) — construct it with whatever key we have
		// (possibly "").
		p, err := provider.NewFromProfile(resolved, apiKey, maxTokens, prompt.ShippedFor(resolved.Adapter))
		if err != nil {
			log.Error("provider: failed to construct, falling back to echo mode", "provider", selected, "err", err)
			srv.SetSuggest(echoMissingKey(resolved.APIKeyEnv))
		} else {
			srv.SetSuggest(suggest.LLM(p, log, emit, rawText))
			// Never log the key itself.
			log.Info("llm mode", "provider", selected, "adapter", resolved.Adapter, "model", resolved.Model)
		}
	}

	if err := srv.Run(ctx); err != nil {
		log.Error("daemon exited", "err", err)
		os.Exit(1)
	}
}

// echoMissingKey stands in for the LLM suggester when a provider is selected
// but its API key isn't set. It never fabricates plausible ghost text: an
// empty buffer gets a shell comment ("# autopilot: set X", harmless if
// accepted); a non-empty buffer gets an empty suffix (no ghost text at all).
func echoMissingKey(keyEnv string) func(context.Context, protocol.Request) (protocol.Reply, error) {
	return func(_ context.Context, req protocol.Request) (protocol.Reply, error) {
		suggestion := req.Buf
		if req.Buf == "" {
			suggestion = "# autopilot: set " + keyEnv
		}
		return protocol.Reply{
			V:          protocol.Version,
			ID:         req.ID,
			Source:     protocol.SourceLLM,
			Suggestion: suggestion,
		}, nil
	}
}

// loadConfig loads config.toml (see configPath) if present, or defaults if
// not. A malformed file is always an error; a *missing* file errors only
// when the path was named explicitly via ZSH_AUTOPILOT_CONFIG — an explicit
// typo should be heard about, but the implicit XDG default being absent
// falls back silently.
func loadConfig() (config.Config, error) {
	path, explicit := configPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) && !explicit {
			return config.Parse([]byte{})
		}
		return config.Config{}, fmt.Errorf("read %s: %w", path, err)
	}
	return config.Parse(data)
}

// configPath resolves the config.toml location and reports whether it was
// named explicitly. ZSH_AUTOPILOT_CONFIG wins when set; otherwise it's
// $XDG_CONFIG_HOME/autopilot/config.toml, falling back to
// ~/.config/autopilot/config.toml.
func configPath() (path string, explicit bool) {
	if p := os.Getenv("ZSH_AUTOPILOT_CONFIG"); p != "" {
		return p, true
	}
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(envOr("HOME", "."), ".config")
	}
	return filepath.Join(configHome, "autopilot", "config.toml"), false
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
