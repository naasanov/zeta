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
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/naasanov/zsh-autopilot/daemon/internal/protocol"
	"github.com/naasanov/zsh-autopilot/daemon/internal/provider"
	"github.com/naasanov/zsh-autopilot/daemon/internal/server"
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

// systemPromptTyping tells the model to emit only the suffix to append to what
// the user has typed. Two properties are load-bearing and tuned by dogfooding
// (design §235):
//   - Spacing: the reply is req.Buf + suffix with nothing inserted between, so
//     the model must supply the leading space itself when the completion starts
//     a new word (otherwise "git add" + "." => "git add.").
//   - Restraint: prefer a short completion and stop before free-form input it
//     cannot know (a commit message, a filename) instead of fabricating one. A
//     partial completion is useful; the whole command is not required.
//
// The model's OUTPUT must stay single-line (provider.Complete cuts at the first
// newline). The prompt itself may span multiple lines. The « » in the examples
// only mark exact output boundaries so leading spaces are visible.
const systemPromptTyping = `You are a shell command autocompletion engine. You receive exactly what the user has typed so far and output the text to append to continue the command — nothing else.

Rules:
- Your output is appended verbatim, with NO separator added. Begin with a space when the completion starts a new word or argument; begin with no space when finishing the current word.
- Never repeat or restate what the user already typed.
- Prefer a SHORT, high-confidence completion. A partial completion is useful — you do NOT need to produce the whole command.
- Never invent specifics you cannot know: commit messages, file names, branch names, URLs, values. Stop right before such free-form input (for example, end at the opening quote).
- Output a single line. No explanation, no markdown, no backticks.
- If nothing useful comes to mind, output nothing.

Examples — everything after the arrow indicates the exact output (leading spaces included)
git ad =>d
git add => .
git add -A => && git commit -m "
git commit -m  => "
docker run => -it `

// systemPromptNextCommand is used when the buffer is empty (KindNextCommand):
// there is nothing to complete, so the model predicts a likely next command.
// Same restraint applies — a short, common command beats a fabricated pipeline.
const systemPromptNextCommand = `You are a shell command prediction engine. The user's prompt is empty, immediately after running a previous command. Output a single likely next command.

Rules:
- Prefer a SHORT, common command over a long speculative one.
- Never invent specifics you cannot know (commit messages, file names, values). Stop before such free-form input (for example, end at the opening quote).
- Output a single line. No explanation, no markdown, no backticks.
- If nothing useful comes to mind, output nothing.`

func main() {
	socket := flag.String("socket", server.DefaultSocket, "unix socket path to listen on")
	verbose := flag.Bool("v", false, "enable debug logging")
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	// compactHandler keeps the dev log panel terse: "HH:MM:SS.mmm L msg key=val"
	// instead of slog's verbose time=/level=/msg= prefix (see logging.go).
	log := slog.New(newCompactHandler(os.Stderr, level))

	srv := server.New(*socket, log)
	srv.Debounce = debounceFromEnv(log)

	// Keys never travel via flags/argv (they'd show up in `ps`); env only.
	if apiKey := os.Getenv("GROQ_API_KEY"); apiKey != "" {
		baseURL := envOr("ZSH_AUTOPILOT_BASE_URL", defaultBaseURL)
		model := envOr("ZSH_AUTOPILOT_MODEL", defaultModel)
		client := provider.NewClient(baseURL, model, apiKey, defaultMaxTokens)
		srv.SetSuggest(llmSuggest(client, log))
		log.Info("llm mode", "base_url", baseURL, "model", model)
	} else {
		log.Info("echo mode: GROQ_API_KEY not set, using placeholder suggestions")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

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

// typingUserPrefix is prepended to req.Buf in the USER turn (not the system
// prompt). Placing the spacing directive right next to the input gives it more
// attention on instruct models than the system prompt alone — empirically this
// fixed the model dropping leading spaces. req.Buf stays at the very end so the
// completion continues directly from it, and the static prefix here is still a
// stable cache prefix for Phase 2 prompt caching.
const typingUserPrefix = "Complete this command, keeping any needed leading space:\n"

// contextBlock renders whatever step-5 context fields (design §7) are present
// on req into a compact "Context:" block for the user turn, one line per
// present field, omitting lines for absent/zero fields entirely (no "git:"
// line without a branch, no "last command" line when LastExit == 0, no
// "recent commands" line with empty History). It returns "" when no context
// fields are present at all, so callers can skip the block cleanly.
//
// This is a pure function (no I/O) so it's unit-testable without a running
// daemon or provider.
func contextBlock(req protocol.Request) string {
	var lines []string
	if req.Cwd != "" {
		lines = append(lines, "- cwd: "+req.Cwd)
	}
	if req.GitBranch != "" {
		branch := "- git: branch " + req.GitBranch
		if req.GitDirty {
			branch += " (dirty)"
		}
		lines = append(lines, branch)
	}
	if req.LastExit != 0 {
		lines = append(lines, "- last command failed (exit "+strconv.Itoa(req.LastExit)+")")
	}
	if len(req.History) > 0 {
		lines = append(lines, "- recent commands: "+strings.Join(req.History, "; "))
	}
	if len(lines) == 0 {
		return ""
	}
	return "Context:\n" + strings.Join(lines, "\n") + "\n\n"
}

// llmSuggest adapts a provider.Client into the server's suggest seam
// (func(ctx, protocol.Request) (protocol.Reply, error)). It builds the
// prompt from req.Buf plus whatever step-5 context fields (design §7) are
// present on the request (see contextBlock), calls the provider, and
// assembles the reply so Suggestion always starts with req.Buf: the zsh
// client strips that exact prefix before painting ghost text, so this
// invariant is load-bearing, not cosmetic.
func llmSuggest(client *provider.Client, log *slog.Logger) func(ctx context.Context, req protocol.Request) (protocol.Reply, error) {
	return func(ctx context.Context, req protocol.Request) (protocol.Reply, error) {
		ctxBlock := contextBlock(req)

		system := systemPromptTyping
		// ctxBlock goes BEFORE the directive+buffer; req.Buf must stay at the
		// very end so the completion continues directly from it (see
		// typingUserPrefix's doc comment).
		user := ctxBlock + typingUserPrefix + req.Buf
		if req.Kind == protocol.KindNextCommand {
			system = systemPromptNextCommand
			user = ctxBlock + "(prompt is empty)"
		}

		// TEMP(debug): dump the exact prompt sent to the LLM — the assembled
		// context block + directive + buffer — so we can eyeball what the model
		// actually sees. Gated behind -v. Remove before shipping.
		if log.Enabled(ctx, slog.LevelDebug) {
			fmt.Fprintf(os.Stderr, "\n=== llm prompt (id=%s) ===\n[system]\n%s\n[user]\n%s\n=== end prompt ===\n\n", req.ID, system, user)
		}

		suffix, err := client.Complete(ctx, system, user)
		if err != nil {
			// Coordinator logs and skips the write on error — graceful
			// degradation, no ghost text for this request.
			return protocol.Reply{}, err
		}
		suffix = strings.TrimRight(suffix, " \t\r\n")

		return protocol.Reply{
			V:          protocol.Version,
			ID:         req.ID,
			Source:     protocol.SourceLLM,
			Suggestion: req.Buf + suffix,
		}, nil
	}
}
