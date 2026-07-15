// Command autopilotd is the zsh-autopilot daemon: a long-running process that
// the zsh client talks to over a Unix domain socket. It holds warm keep-alive
// connections to LLM providers, debounces keystroke-driven requests, cancels
// stale in-flight requests, and streams suggestions back to the shell.
//
// See .docs/zeta_design_doc_v3.md §3 for the architecture. Phase 1 step 2
// built only the socket listener and process skeleton, answering every
// request with a canned echo suggestion (internal/server). Step 4 wires in a
// real LLM-backed suggester (internal/provider) when an API key is present,
// prompting with just the current buffer for now; the richer context
// pipeline (cwd/git/history) is a later step.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/naasanov/zsh-autopilot/daemon/internal/logging"
	"github.com/naasanov/zsh-autopilot/daemon/internal/metrics"
	"github.com/naasanov/zsh-autopilot/daemon/internal/provider"
	"github.com/naasanov/zsh-autopilot/daemon/internal/server"
	"github.com/naasanov/zsh-autopilot/daemon/internal/suggest"
)

// Groq defaults (design §4/§6): Groq is the fastest OpenAI-compatible option
// and the target for Phase 1. Env vars let a user point at any other
// OpenAI-shaped endpoint (OpenAI itself, Together, local Ollama, ...) without
// a code change; Phase 2 replaces this with TOML profiles.
const (
	defaultBaseURL = "https://api.groq.com/openai/v1"
	defaultModel   = "llama-3.3-70b-versatile"
	// defaultMaxTokens keeps output short (design §5): a shell completion is
	// a handful of words, not prose.
	defaultMaxTokens = 48
)

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

	srv := server.New(*socket, log)
	srv.Debounce = debounceFromEnv(log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// METRICS(§12): dev-only JSONL event log, dogfooding-only and stripped in
	// Phase 3 (see internal/metrics). Fully independent of the provider gate
	// below — metrics can run even in echo mode, but the "request" event
	// only exists on the LLM path since only suggest.LLM has anything to
	// report.
	var emit func(metrics.RequestEvent)
	if cfg, ok := metrics.ConfigFromEnv(); ok {
		mlog, err := metrics.New(cfg.LogPath, cfg.User)
		if err != nil {
			log.Error("metrics: failed to open log, metrics disabled", "path", cfg.LogPath, "err", err)
		} else {
			defer mlog.Close()
			go func() {
				if err := metrics.Serve(ctx, cfg.SocketPath, mlog, log); err != nil {
					log.Error("metrics: serve exited", "err", err)
				}
			}()
			emit = mlog.EmitRequest
			log.Info("metrics enabled", "log", cfg.LogPath, "socket", cfg.SocketPath, "user", cfg.User)
		}
	}

	// Keys never travel via flags/argv (they'd show up in `ps`); env only.
	if apiKey := os.Getenv("GROQ_API_KEY"); apiKey != "" {
		baseURL := envOr("ZSH_AUTOPILOT_BASE_URL", defaultBaseURL)
		model := envOr("ZSH_AUTOPILOT_MODEL", defaultModel)
		client := provider.NewClient(baseURL, model, apiKey, defaultMaxTokens)
		srv.SetSuggest(suggest.LLM(client, log, emit))
		log.Info("llm mode", "base_url", baseURL, "model", model)
	} else {
		log.Info("echo mode: GROQ_API_KEY not set, using placeholder suggestions")
	}

	if err := srv.Run(ctx); err != nil {
		log.Error("daemon exited", "err", err)
		os.Exit(1)
	}
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
// the provider. Falls back to server.DefaultDebounce (100ms) if unset,
// empty, or not a valid non-negative integer.
func debounceFromEnv(log *slog.Logger) time.Duration {
	v := os.Getenv("ZSH_AUTOPILOT_DEBOUNCE_MS")
	if v == "" {
		return server.DefaultDebounce
	}
	ms, err := strconv.Atoi(v)
	if err != nil || ms < 0 {
		log.Error("invalid ZSH_AUTOPILOT_DEBOUNCE_MS, using default", "value", v, "default_ms", server.DefaultDebounce.Milliseconds())
		return server.DefaultDebounce
	}
	return time.Duration(ms) * time.Millisecond
}
