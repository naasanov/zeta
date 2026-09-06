package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/naasanov/zsh-autopilot/daemon/internal/history"
	"github.com/naasanov/zsh-autopilot/daemon/internal/protocol"
)

// writeTempConfig writes body to a temp config.toml and points
// ZSH_AUTOPILOT_CONFIG at it for the duration of the test.
func writeTempConfig(t *testing.T, body string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	t.Setenv("ZSH_AUTOPILOT_CONFIG", path)
}

// TestLoadConfig_MissingExplicitPathErrors confirms a typo'd ZSH_AUTOPILOT_CONFIG
// fails loudly rather than silently falling back to defaults.
func TestLoadConfig_MissingExplicitPathErrors(t *testing.T) {
	t.Setenv("ZSH_AUTOPILOT_CONFIG", filepath.Join(t.TempDir(), "does-not-exist.toml"))

	if _, err := loadConfig(); err == nil {
		t.Error("loadConfig() error = nil, want error for a named-but-missing config path")
	}
}

// TestLoadConfig_EmptyFileAppliesDefaults confirms an empty config.toml (no
// profiles at all) still loads cleanly with the package defaults — presets
// resolve on demand via Config.Resolve now, so there's no built-in profile
// left to seed.
func TestLoadConfig_EmptyFileAppliesDefaults(t *testing.T) {
	writeTempConfig(t, "")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if cfg.DebounceMS != 100 {
		t.Errorf("DebounceMS = %v, want 100", cfg.DebounceMS)
	}
	if cfg.MaxTokens != 48 {
		t.Errorf("MaxTokens = %v, want 48", cfg.MaxTokens)
	}
	if cfg.DefaultProfile != "" {
		t.Errorf("DefaultProfile = %q, want empty", cfg.DefaultProfile)
	}
}

// TestLoadConfig_ImplicitMissingFileIsFine confirms the implicit XDG default
// path simply being absent (no ZSH_AUTOPILOT_CONFIG set, and presumably no
// ~/.config/autopilot/config.toml) falls back silently rather than erroring.
func TestLoadConfig_ImplicitMissingFileIsFine(t *testing.T) {
	t.Setenv("ZSH_AUTOPILOT_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // empty dir, no autopilot/config.toml in it

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if cfg.DebounceMS != 100 {
		t.Errorf("DebounceMS = %v, want 100", cfg.DebounceMS)
	}
}

// TestNoticeSuggest asserts the notice-only suggest stub never fabricates
// ghost text and carries the notice text/kind through on every request,
// regardless of buffer contents.
func TestNoticeSuggest(t *testing.T) {
	fn := noticeSuggest("no API key: set ZSH_AUTOPILOT_GROQ_KEY", "no_key")

	t.Run("empty buffer gets no ghost text, only a notice", func(t *testing.T) {
		reply, err := fn(context.Background(), protocol.Request{
			V: protocol.Version, ID: "1", Kind: protocol.KindNextCommand, Buf: "",
		})
		if err != nil {
			t.Fatalf("noticeSuggest fn err = %v, want nil", err)
		}
		if reply.Suggestion != "" {
			t.Errorf("Suggestion = %q, want empty", reply.Suggestion)
		}
		if reply.Notice != "no API key: set ZSH_AUTOPILOT_GROQ_KEY" || reply.NoticeKind != "no_key" {
			t.Errorf("reply = %+v, want Notice/NoticeKind set", reply)
		}
		if reply.ID != "1" || reply.Source != protocol.SourceLLM {
			t.Errorf("reply = %+v, want ID=1 Source=llm", reply)
		}
	})

	t.Run("non-empty buffer still gets no ghost text", func(t *testing.T) {
		reply, err := fn(context.Background(), protocol.Request{
			V: protocol.Version, ID: "2", Kind: protocol.KindTyping, Buf: "git status",
		})
		if err != nil {
			t.Fatalf("noticeSuggest fn err = %v, want nil", err)
		}
		if reply.Suggestion != "" {
			t.Errorf("Suggestion = %q, want empty (no suffix appended)", reply.Suggestion)
		}
	})
}

// newTestHistoryStore returns a *history.Store backed by a fresh temp
// journal, with no $HISTFILE bootstrap.
func newTestHistoryStore(t *testing.T) *history.Store {
	t.Helper()
	h, err := history.New(history.Config{
		JournalPath: filepath.Join(t.TempDir(), "history.jsonl"),
	})
	if err != nil {
		t.Fatalf("history.New: %v", err)
	}
	t.Cleanup(func() { h.Close() })
	return h
}

// TestEnrich_FillsHistoryInOrderWithAlignedCwds confirms enrich populates
// req.History/HistoryCwds from the store's pool, index-aligned, before
// calling next.
func TestEnrich_FillsHistoryInOrderWithAlignedCwds(t *testing.T) {
	h := newTestHistoryStore(t)
	h.Record(history.Entry{Cmd: "git status", Cwd: "/x/proj", Session: "s"})
	h.Record(history.Entry{Cmd: "npm install", Cwd: "/x/proj", Session: "s"})
	h.Record(history.Entry{Cmd: "go build ./...", Cwd: "/x/gotool", Session: "s"})

	var gotReq protocol.Request
	next := func(_ context.Context, req protocol.Request) (protocol.Reply, error) {
		gotReq = req
		return protocol.Reply{ID: req.ID}, nil
	}

	fn := enrich(h, history.DefaultPoolN, next)
	if _, err := fn(context.Background(), protocol.Request{ID: "1", Cwd: "/x/proj"}); err != nil {
		t.Fatalf("enrich fn err = %v", err)
	}

	wantHistory := []string{"git status", "npm install", "go build ./..."}
	if len(gotReq.History) != len(wantHistory) {
		t.Fatalf("History = %v, want %v", gotReq.History, wantHistory)
	}
	for i, cmd := range wantHistory {
		if gotReq.History[i] != cmd {
			t.Errorf("History[%d] = %q, want %q", i, gotReq.History[i], cmd)
		}
	}
	wantCwds := []string{"/x/proj", "/x/proj", "/x/gotool"}
	if len(gotReq.HistoryCwds) != len(wantCwds) {
		t.Fatalf("HistoryCwds = %v, want %v", gotReq.HistoryCwds, wantCwds)
	}
	for i, cwd := range wantCwds {
		if gotReq.HistoryCwds[i] != cwd {
			t.Errorf("HistoryCwds[%d] = %q, want %q", i, gotReq.HistoryCwds[i], cwd)
		}
	}
}

// TestEnrich_NilStoreIsPassthrough confirms a nil store leaves the request
// untouched and still delegates to next.
func TestEnrich_NilStoreIsPassthrough(t *testing.T) {
	var calledWith protocol.Request
	called := false
	next := func(_ context.Context, req protocol.Request) (protocol.Reply, error) {
		called = true
		calledWith = req
		return protocol.Reply{ID: req.ID}, nil
	}

	fn := enrich(nil, history.DefaultPoolN, next)
	req := protocol.Request{ID: "1", Cwd: "/x/proj", Buf: "git st"}
	if _, err := fn(context.Background(), req); err != nil {
		t.Fatalf("enrich fn err = %v", err)
	}

	if !called {
		t.Fatal("next was not called")
	}
	if calledWith.History != nil || calledWith.HistoryCwds != nil {
		t.Errorf("History/HistoryCwds = %v/%v, want nil/nil (untouched)", calledWith.History, calledWith.HistoryCwds)
	}
	if calledWith.Buf != req.Buf || calledWith.Cwd != req.Cwd || calledWith.ID != req.ID {
		t.Errorf("request fields altered: got %+v, want %+v", calledWith, req)
	}
}

// TestSessionOf covers the "<session>.<seq>" split and the no-'.' fallback.
func TestSessionOf(t *testing.T) {
	cases := []struct {
		id   string
		want string
	}{
		{"abc123.5", "abc123"},
		{"sess.with.dots.9", "sess.with.dots"},
		{"noseparator", "noseparator"},
		{"", ""},
	}
	for _, c := range cases {
		if got := sessionOf(c.id); got != c.want {
			t.Errorf("sessionOf(%q) = %q, want %q", c.id, got, c.want)
		}
	}
}

// TestResolveHistfilePath_AppScopedWinsOverAmbient confirms the app-scoped
// var is preferred over the ambient $HISTFILE, matching this repo's
// convention of not borrowing an ambient var when an app-scoped one exists.
func TestResolveHistfilePath_AppScopedWinsOverAmbient(t *testing.T) {
	t.Setenv("ZSH_AUTOPILOT_HISTFILE", "/app/histfile")
	t.Setenv("HISTFILE", "/ambient/histfile")

	if got := resolveHistfilePath(); got != "/app/histfile" {
		t.Errorf("resolveHistfilePath() = %q, want %q", got, "/app/histfile")
	}
}

// TestResolveHistfilePath_AmbientWinsOverFallback confirms $HISTFILE is used
// when the app-scoped var is unset.
func TestResolveHistfilePath_AmbientWinsOverFallback(t *testing.T) {
	t.Setenv("ZSH_AUTOPILOT_HISTFILE", "")
	t.Setenv("HISTFILE", "/ambient/histfile")

	if got := resolveHistfilePath(); got != "/ambient/histfile" {
		t.Errorf("resolveHistfilePath() = %q, want %q", got, "/ambient/histfile")
	}
}

// TestResolveHistfilePath_FallsBackToHome confirms ~/.zsh_history is used
// when neither the app-scoped var nor $HISTFILE is set.
func TestResolveHistfilePath_FallsBackToHome(t *testing.T) {
	t.Setenv("ZSH_AUTOPILOT_HISTFILE", "")
	t.Setenv("HISTFILE", "")
	t.Setenv("HOME", "/home/testuser")

	want := filepath.Join("/home/testuser", ".zsh_history")
	if got := resolveHistfilePath(); got != want {
		t.Errorf("resolveHistfilePath() = %q, want %q", got, want)
	}
}
