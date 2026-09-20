// Command autopilotd is the zsh-autopilot daemon, a long-running process the
// zsh client talks to over a Unix domain socket.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/naasanov/zsh-autopilot/daemon/internal/config"
	"github.com/naasanov/zsh-autopilot/daemon/internal/history"
	"github.com/naasanov/zsh-autopilot/daemon/internal/logging"
	"github.com/naasanov/zsh-autopilot/daemon/internal/metrics"
	"github.com/naasanov/zsh-autopilot/daemon/internal/prompt"
	"github.com/naasanov/zsh-autopilot/daemon/internal/protocol"
	"github.com/naasanov/zsh-autopilot/daemon/internal/provider"
	"github.com/naasanov/zsh-autopilot/daemon/internal/server"
	"github.com/naasanov/zsh-autopilot/daemon/internal/suggest"
)

const knownProviders = "anthropic, codestral, groq, ollama, openai"

func main() {
	socket := flag.String("socket", envOr("ZSH_AUTOPILOT_SOCKET", server.DefaultSocket), "unix socket path to listen on (default $ZSH_AUTOPILOT_SOCKET)")
	histfile := flag.String("histfile", resolveHistfilePath(), "shell HISTFILE to bootstrap history from (default $ZSH_AUTOPILOT_HISTFILE, $HISTFILE, or ~/.zsh_history)")
	historyJournal := flag.String("history-journal", envOr("ZSH_AUTOPILOT_HISTORY_JOURNAL", historyJournalPath()), "path to the daemon-owned history journal (default $ZSH_AUTOPILOT_HISTORY_JOURNAL)")
	verbose := flag.Bool("v", envTrue("ZSH_AUTOPILOT_DEBUG"), "enable debug logging (default $ZSH_AUTOPILOT_DEBUG)")
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
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

	var emit func(metrics.RequestEvent)
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
				log.Warn("raw-text metrics capture ENABLED (default) — command buffers, suggestions and cwd are written verbatim to the event log; set "+metrics.EnvRawText+"=0 to disable", "log", mcfg.LogPath)
			}
		}
	}

	store, err := history.New(history.Config{
		JournalPath:  *historyJournal,
		HistfilePath: *histfile,
		Log:          log,
	})
	if err != nil {
		log.Warn("history: failed to open store, continuing without history", "err", err)
		store = nil
	}
	if store != nil {
		defer store.Close()
		srv.SetRecord(recordHistory(store))
		log.Info("history: store opened", "journal", *historyJournal, "histfile", *histfile, "entries", store.Len())
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
	srv.SetNotice(suggest.NoticeFor(selected, resolved.APIKeyEnv))

	resolved.BaseURL = envOr("ZSH_AUTOPILOT_BASE_URL", resolved.BaseURL)
	resolved.Model = envOr("ZSH_AUTOPILOT_MODEL", resolved.Model)

	maxTokens := cfg.MaxTokens

	// Keys never travel via flags/argv (they'd show up in `ps`); env (or
	// api_key_cmd, itself env/exec-based) only.
	apiKey, err := resolved.ResolveKey()
	if err != nil {
		log.Error("config: failed to resolve api key", "provider", selected, "err", err)
		apiKey = ""
	}
	needsKey := resolved.NeedsKey()

	switch {
	case needsKey && apiKey == "":
		srv.SetSuggest(noticeSuggest("no API key: set "+resolved.APIKeyEnv, "no_key"))
		log.Error("no suggestions: API key not set", "key_env", resolved.APIKeyEnv)
	default:
		p, err := provider.NewFromProfile(resolved, apiKey, maxTokens, prompt.ShippedFor(resolved.Adapter))
		if err != nil {
			log.Error("no suggestions: provider failed to construct", "provider", selected, "err", err)
			srv.SetSuggest(noticeSuggest("provider init failed: "+err.Error(), "provider_init"))
		} else {
			srv.SetSuggest(enrich(store, history.DefaultPoolN, suggest.LLM(p, log, emit, rawText)))
			// Never log the key itself.
			log.Info("llm mode", "provider", selected, "adapter", resolved.Adapter, "model", resolved.Model)
		}
	}

	if err := srv.Run(ctx); err != nil {
		log.Error("daemon exited", "err", err)
		os.Exit(1)
	}
}

func noticeSuggest(text, kind string) func(context.Context, protocol.Request) (protocol.Reply, error) {
	return func(_ context.Context, req protocol.Request) (protocol.Reply, error) {
		return protocol.Reply{
			V:          protocol.Version,
			ID:         req.ID,
			Source:     protocol.SourceLLM,
			Suggestion: "",
			Notice:     text,
			NoticeKind: kind,
		}, nil
	}
}

// loadConfig loads config.toml if present, or defaults if not. A missing
// file errors only when the path was named explicitly via
// ZSH_AUTOPILOT_CONFIG.
func loadConfig() (config.Config, error) {
	path, explicit := configPath()
	return config.Load(path, explicit)
}

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

func historyJournalPath() string {
	stateHome := os.Getenv("XDG_STATE_HOME")
	if stateHome == "" {
		stateHome = filepath.Join(envOr("HOME", "."), ".local", "state")
	}
	return filepath.Join(stateHome, "autopilot", "history.jsonl")
}

func resolveHistfilePath() string {
	if v := os.Getenv("ZSH_AUTOPILOT_HISTFILE"); v != "" {
		return v
	}
	if v := os.Getenv("HISTFILE"); v != "" {
		return v
	}
	return filepath.Join(envOr("HOME", "."), ".zsh_history")
}

func envTrue(key string) bool {
	v := os.Getenv(key)
	return v != "" && v != "0" && v != "false"
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

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

func recordHistory(h *history.Store) func(protocol.Request) {
	return func(req protocol.Request) {
		if req.Cmd == "" {
			return
		}
		h.Record(history.Entry{
			Cmd:     req.Cmd,
			Cwd:     req.Cwd,
			Session: sessionOf(req.ID),
			Ts:      req.Ts,
		})
	}
}

// sessionOf splits a "<session>.<seq>" request id at the last '.'; an id
// with no '.' is treated as bare session.
func sessionOf(id string) string {
	if i := strings.LastIndexByte(id, '.'); i >= 0 {
		return id[:i]
	}
	return id
}

type suggestFn func(context.Context, protocol.Request) (protocol.Reply, error)

func enrich(h *history.Store, n int, next suggestFn) suggestFn {
	if h == nil {
		return next
	}
	return func(ctx context.Context, req protocol.Request) (protocol.Reply, error) {
		pool := h.Select(history.Query{Cwd: req.Cwd, N: n})
		entries := make([]protocol.HistoryEntry, len(pool))
		for i, e := range pool {
			entries[i] = protocol.HistoryEntry{Cmd: e.Cmd, Cwd: e.Cwd}
		}
		req.SetHistoryEntries(entries)
		return next(ctx, req)
	}
}
