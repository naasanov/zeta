package eval

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/naasanov/zsh-autopilot/daemon/internal/protocol"
)

// ---- fakeJudge: a Judge test double, no network -----------------------------

// fakeJudge is a scripted Judge used to test the cache and JudgeGrader
// without ever touching the network. Calls is the number of times Judge was
// actually invoked (a cache hit must not increment it).
type fakeJudge struct {
	mu      sync.Mutex
	calls   int
	verdict JudgeVerdict
	err     error
	name    string

	// checkCtx, if set, is called first so cancellation tests can assert the
	// ctx passed all the way through.
	checkCtx func(context.Context) error
}

func (f *fakeJudge) Name() string {
	if f.name == "" {
		return "fake-judge"
	}
	return f.name
}

func (f *fakeJudge) Judge(ctx context.Context, in JudgeInput) (JudgeVerdict, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()

	if f.checkCtx != nil {
		if err := f.checkCtx(ctx); err != nil {
			return JudgeVerdict{}, err
		}
	}
	if f.err != nil {
		return JudgeVerdict{}, f.err
	}
	return f.verdict, nil
}

func (f *fakeJudge) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// ---- Prompt rendering: blinding + content -----------------------------------

// bannedProviderTokens are provider/model family names that must NEVER
// appear anywhere in a rendered judge prompt (system or user turn) — the
// judge must be blind to which system produced the suggestion it grades.
var bannedProviderTokens = []string{
	"codestral", "anthropic", "claude", "groq", "llama", "mistral", "openai", "gemini",
}

func TestJudgePrompt_Blind(t *testing.T) {
	in := JudgeInput{
		Rubric: "Is this a plausible next command?",
		Req: protocol.Request{
			Kind:      protocol.KindNextCommand,
			Cwd:       "/x/gotool",
			GitBranch: "main",
			GitDirty:  true,
			LastExit:  1,
			History:   []string{"git tag v0.1.6", "git push origin v0.1.6"},
			DirEntries: []string{
				"go.mod", "main.go",
			},
		},
		Suggestion: "git push origin v0.1.7",
	}

	rendered := judgeSystemPrompt + "\n" + renderJudgeUser(in)
	low := strings.ToLower(rendered)
	for _, tok := range bannedProviderTokens {
		if strings.Contains(low, tok) {
			t.Errorf("rendered judge prompt contains banned provider/model token %q:\n%s", tok, rendered)
		}
	}
}

func TestJudgePrompt_ContainsRubricContextAndSuggestion(t *testing.T) {
	in := JudgeInput{
		Rubric: "UNIQUE_RUBRIC_MARKER",
		Req: protocol.Request{
			Kind:      protocol.KindNextCommand,
			Cwd:       "/x/gotool",
			GitBranch: "main",
			GitDirty:  true,
			LastExit:  7,
			History:   []string{"go build ./...", "go test ./..."},
			DirEntries: []string{
				"go.mod", "main.go",
			},
			Buf: "some-buffer",
		},
		Suggestion: "UNIQUE_SUGGESTION_MARKER",
	}

	got := renderJudgeUser(in)

	for _, want := range []string{
		"UNIQUE_RUBRIC_MARKER",
		"UNIQUE_SUGGESTION_MARKER",
		"/x/gotool",
		"main",
		"go build ./...",
		"go test ./...",
		"go.mod",
		"some-buffer",
		"7", // last exit code
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered judge prompt missing %q:\n%s", want, got)
		}
	}
}

func TestJudgePrompt_OmitsZeroFields(t *testing.T) {
	// A Request with no context set (E8-shaped) should not print misleading
	// zero-value context lines like "last exit code: 0" or an empty branch.
	in := JudgeInput{
		Rubric:     "does it abstain",
		Req:        protocol.Request{Kind: protocol.KindNextCommand},
		Suggestion: "",
	}
	got := renderJudgeUser(in)
	for _, unwanted := range []string{"git branch:", "last exit code:", "directory entries:", "recent command history"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("rendered judge prompt should omit absent context field, found %q:\n%s", unwanted, got)
		}
	}
}

// ---- Verdict parsing ---------------------------------------------------------

func TestParseVerdict_Pass(t *testing.T) {
	v, err := parseVerdict(`{"verdict":"pass","reason":"looks good"}`)
	if err != nil {
		t.Fatalf("parseVerdict: unexpected error: %v", err)
	}
	if !v.Pass {
		t.Error("want Pass=true")
	}
	if v.Reason != "looks good" {
		t.Errorf("Reason = %q, want %q", v.Reason, "looks good")
	}
}

func TestParseVerdict_Fail(t *testing.T) {
	v, err := parseVerdict(`{"verdict":"fail","reason":"nonsense"}`)
	if err != nil {
		t.Fatalf("parseVerdict: unexpected error: %v", err)
	}
	if v.Pass {
		t.Error("want Pass=false")
	}
}

func TestParseVerdict_MalformedJSON(t *testing.T) {
	if _, err := parseVerdict(`{"verdict": "pass",`); err == nil {
		t.Fatal("want error for malformed JSON, got nil")
	}
}

func TestParseVerdict_Prose(t *testing.T) {
	if _, err := parseVerdict("Sure, I think this suggestion passes the rubric because it looks reasonable."); err == nil {
		t.Fatal("want error for prose response, got nil")
	}
}

func TestParseVerdict_MissingVerdictField(t *testing.T) {
	if _, err := parseVerdict(`{"reason":"no verdict field here"}`); err == nil {
		t.Fatal("want error for missing verdict field, got nil")
	}
}

func TestParseVerdict_MissingReasonField(t *testing.T) {
	if _, err := parseVerdict(`{"verdict":"pass"}`); err == nil {
		t.Fatal("want error for missing reason field, got nil")
	}
}

func TestParseVerdict_UnrecognizedVerdictValue(t *testing.T) {
	if _, err := parseVerdict(`{"verdict":"maybe","reason":"unsure"}`); err == nil {
		t.Fatal("want error for unrecognized verdict value, got nil")
	}
}

func TestParseVerdict_Empty(t *testing.T) {
	if _, err := parseVerdict(""); err == nil {
		t.Fatal("want error for empty response, got nil")
	}
}

func TestParseVerdict_TrailingGarbage(t *testing.T) {
	if _, err := parseVerdict(`{"verdict":"pass","reason":"ok"} and also some more text`); err == nil {
		t.Fatal("want error for trailing content after the JSON value, got nil")
	}
}

// ---- Cache: miss/hit/version/corruption/bypass ------------------------------

func TestJudgeCache_MissThenHit(t *testing.T) {
	dir := t.TempDir()
	c := NewJudgeCache(dir, false)

	key := cacheKey("C1", "rubric text", "model-a", "suggestion")

	if _, ok := c.Get(key); ok {
		t.Fatal("expected miss on empty cache")
	}

	want := JudgeVerdict{Pass: true, Reason: "because"}
	c.Set(key, want)

	got, ok := c.Get(key)
	if !ok {
		t.Fatal("expected hit after Set")
	}
	if got != want {
		t.Errorf("Get() = %+v, want %+v", got, want)
	}
}

func TestJudgeCache_RubricChangeMisses(t *testing.T) {
	dir := t.TempDir()
	c := NewJudgeCache(dir, false)

	k1 := cacheKey("C1", "rubric v1", "model-a", "suggestion")
	k2 := cacheKey("C1", "rubric v2", "model-a", "suggestion")

	c.Set(k1, JudgeVerdict{Pass: true, Reason: "v1"})

	if _, ok := c.Get(k2); ok {
		t.Error("changing the rubric text should invalidate the cached verdict (different key), got a hit")
	}
}

func TestJudgeCache_CorruptEntryIsMissNotCrash(t *testing.T) {
	dir := t.TempDir()
	c := NewJudgeCache(dir, false)

	key := cacheKey("C1", "rubric", "model-a", "suggestion")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, key+".json"), []byte("{not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, ok := c.Get(key); ok {
		t.Error("corrupt cache file should be a miss, got a hit")
	}
}

func TestJudgeCache_UnreadableDirIsMissNotCrash(t *testing.T) {
	// A cache rooted at a path that doesn't exist at all must behave like a
	// miss (Get), never panic or error out of band.
	c := NewJudgeCache(filepath.Join(t.TempDir(), "does", "not", "exist"), false)
	if _, ok := c.Get("anykey"); ok {
		t.Error("expected miss for a nonexistent cache dir")
	}
}

func TestJudgeCache_Bypass(t *testing.T) {
	dir := t.TempDir()
	key := cacheKey("C1", "rubric", "model-a", "suggestion")

	normal := NewJudgeCache(dir, false)
	normal.Set(key, JudgeVerdict{Pass: true, Reason: "cached"})

	bypass := NewJudgeCache(dir, true)
	if _, ok := bypass.Get(key); ok {
		t.Error("a bypass cache must always miss on Get, even with a populated entry on disk")
	}

	// Bypass Set should still write (a "refresh" run), so a subsequent
	// normal-mode cache observes the refreshed value.
	bypass.Set(key, JudgeVerdict{Pass: false, Reason: "refreshed"})
	got, ok := normal.Get(key)
	if !ok {
		t.Fatal("expected the refreshed value to be visible to a normal-mode cache")
	}
	if got.Pass || got.Reason != "refreshed" {
		t.Errorf("Get() after bypass refresh = %+v, want the refreshed verdict", got)
	}
}

func TestJudgeCache_NilCacheIsSafeMiss(t *testing.T) {
	var c *JudgeCache
	if _, ok := c.Get("anykey"); ok {
		t.Error("nil *JudgeCache.Get must report a miss, not panic")
	}
	c.Set("anykey", JudgeVerdict{Pass: true}) // must not panic
}

// ---- JudgeGrader: caching/dedup, ctx forwarding, pass mapping ---------------

func req() protocol.Request {
	return protocol.Request{Kind: protocol.KindNextCommand, History: []string{"git status"}}
}

func TestJudgeGrader_CacheHitAvoidsSecondCall(t *testing.T) {
	dir := t.TempDir()
	cache := NewJudgeCache(dir, false)
	fj := &fakeJudge{verdict: JudgeVerdict{Pass: true, Reason: "ok"}, name: "fake-1"}

	g := NewJudgeGrader("C-test", "label", "the rubric", fj, cache)

	present, err := g.Grade(context.Background(), req(), " suggestion-a")
	if err != nil {
		t.Fatalf("first Grade: unexpected error: %v", err)
	}
	if !present {
		t.Error("want present=true for a Pass verdict")
	}

	present2, err := g.Grade(context.Background(), req(), " suggestion-a")
	if err != nil {
		t.Fatalf("second Grade: unexpected error: %v", err)
	}
	if !present2 {
		t.Error("want present=true from the cache too")
	}

	if got := fj.callCount(); got != 1 {
		t.Errorf("judge was called %d times, want exactly 1 (second call should be a cache hit)", got)
	}
}

func TestJudgeGrader_DifferentSuggestionMisses(t *testing.T) {
	dir := t.TempDir()
	cache := NewJudgeCache(dir, false)
	fj := &fakeJudge{verdict: JudgeVerdict{Pass: true}, name: "fake-1"}
	g := NewJudgeGrader("C-test", "label", "the rubric", fj, cache)

	if _, err := g.Grade(context.Background(), req(), " a"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Grade(context.Background(), req(), " b"); err != nil {
		t.Fatal(err)
	}
	if got := fj.callCount(); got != 2 {
		t.Errorf("judge was called %d times for 2 distinct suggestions, want 2", got)
	}
}

func TestJudgeGrader_FailVerdictMapsToNotPresent(t *testing.T) {
	fj := &fakeJudge{verdict: JudgeVerdict{Pass: false, Reason: "nope"}}
	g := NewJudgeGrader("C-test", "label", "rubric", fj, nil)

	present, err := g.Grade(context.Background(), req(), " x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if present {
		t.Error("want present=false for a Fail verdict")
	}
}

func TestJudgeGrader_JudgeErrorPropagates(t *testing.T) {
	wantErr := errors.New("boom")
	fj := &fakeJudge{err: wantErr}
	g := NewJudgeGrader("C-test", "label", "rubric", fj, nil)

	_, err := g.Grade(context.Background(), req(), " x")
	if err == nil {
		t.Fatal("want an error when the judge itself errors, got nil")
	}
}

func TestJudgeGrader_ForwardsCancelledContext(t *testing.T) {
	fj := &fakeJudge{
		checkCtx: func(ctx context.Context) error { return ctx.Err() },
		verdict:  JudgeVerdict{Pass: true},
	}
	g := NewJudgeGrader("C-test", "label", "rubric", fj, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := g.Grade(ctx, req(), " x")
	if err == nil {
		t.Fatal("want an error for a cancelled context, got nil (and definitely not a verdict)")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want it to wrap context.Canceled", err)
	}
}

func TestJudgeGrader_UnconfiguredReturnsErrorNotPanicOrPass(t *testing.T) {
	wantErr := errors.New("no api key")
	g := NewUnconfiguredJudgeGrader("C3", "label", wantErr)

	present, err := g.Grade(context.Background(), req(), " anything")
	if err == nil {
		t.Fatal("want an error from an unconfigured judge grader, got nil")
	}
	if present {
		t.Error("an unconfigured judge must never report present=true (that would be a silent pass)")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want it to wrap %v", err, wantErr)
	}
}

func TestJudgeGrader_NameIsStableAndCaseScoped(t *testing.T) {
	fj := &fakeJudge{}
	g1 := NewJudgeGrader("C3", "my-label", "rubric", fj, nil)
	g2 := NewJudgeGrader("C3", "my-label", "a totally different rubric", fj, nil)
	if g1.Name() != g2.Name() {
		t.Errorf("Name() must be stable across grader instances for the same case/label: %q vs %q", g1.Name(), g2.Name())
	}
	g3 := NewJudgeGrader("E7", "my-label", "rubric", fj, nil)
	if g1.Name() == g3.Name() {
		t.Errorf("Name() must be case-scoped; C3 and E7 graders with the same label collided: %q", g1.Name())
	}
}

// ---- Config resolution -------------------------------------------------------

func TestJudgeConfigFromEnv_Defaults(t *testing.T) {
	t.Setenv("ZSH_AUTOPILOT_EVAL_JUDGE_KEY", "")
	t.Setenv("ZSH_AUTOPILOT_EVAL_JUDGE_MODEL", "")
	t.Setenv("ZSH_AUTOPILOT_EVAL_JUDGE_BASE_URL", "")
	t.Setenv("ZSH_AUTOPILOT_EVAL_JUDGE_CACHE", "")

	cfg := JudgeConfigFromEnv()
	if cfg.APIKey != "" {
		t.Errorf("APIKey = %q, want empty when unset", cfg.APIKey)
	}
	if cfg.Model != defaultJudgeModel {
		t.Errorf("Model = %q, want default %q", cfg.Model, defaultJudgeModel)
	}
	if cfg.BaseURL != defaultJudgeBaseURL {
		t.Errorf("BaseURL = %q, want default %q", cfg.BaseURL, defaultJudgeBaseURL)
	}
	if cfg.CacheDir == "" {
		t.Error("CacheDir should default to a nonempty path")
	}
}

func TestJudgeConfigFromEnv_Overrides(t *testing.T) {
	t.Setenv("ZSH_AUTOPILOT_EVAL_JUDGE_KEY", "test-key")
	t.Setenv("ZSH_AUTOPILOT_EVAL_JUDGE_MODEL", "custom-model")
	t.Setenv("ZSH_AUTOPILOT_EVAL_JUDGE_BASE_URL", "https://example.test/v1/")
	t.Setenv("ZSH_AUTOPILOT_EVAL_JUDGE_CACHE", "/tmp/custom-cache-dir")

	cfg := JudgeConfigFromEnv()
	if cfg.APIKey != "test-key" {
		t.Errorf("APIKey = %q, want %q", cfg.APIKey, "test-key")
	}
	if cfg.Model != "custom-model" {
		t.Errorf("Model = %q, want %q", cfg.Model, "custom-model")
	}
	if cfg.BaseURL != "https://example.test/v1/" {
		t.Errorf("BaseURL = %q, want %q", cfg.BaseURL, "https://example.test/v1/")
	}
	if cfg.CacheDir != "/tmp/custom-cache-dir" {
		t.Errorf("CacheDir = %q, want %q", cfg.CacheDir, "/tmp/custom-cache-dir")
	}
}

func TestNewGeminiJudge_RequiresAPIKey(t *testing.T) {
	_, err := NewGeminiJudge(JudgeConfig{APIKey: ""})
	if err == nil {
		t.Fatal("want an error building a judge with no API key, got nil")
	}
}

// ---- Wire shape: exercised via httptest, never the real network ------------

// TestGeminiJudge_WireShape drives geminiJudge.Judge against a local
// httptest.Server standing in for the OpenAI-compatible endpoint, to check
// the request/response shape (structured output, temperature 0, model id)
// without ever hitting a real network.
func TestGeminiJudge_WireShape(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "test",
			"object": "chat.completion",
			"created": 0,
			"model": "test-model",
			"choices": [{
				"index": 0,
				"finish_reason": "stop",
				"message": {"role": "assistant", "content": "{\"verdict\":\"pass\",\"reason\":\"looks fine\"}"}
			}]
		}`))
	}))
	defer srv.Close()

	judge, err := NewGeminiJudge(JudgeConfig{
		APIKey:  "test-key",
		Model:   "test-model",
		BaseURL: srv.URL + "/",
	})
	if err != nil {
		t.Fatalf("NewGeminiJudge: %v", err)
	}

	v, err := judge.Judge(context.Background(), JudgeInput{
		Rubric:     "is this plausible",
		Req:        req(),
		Suggestion: " git push",
	})
	if err != nil {
		t.Fatalf("Judge: unexpected error: %v", err)
	}
	if !v.Pass || v.Reason != "looks fine" {
		t.Errorf("Judge() = %+v, want Pass=true Reason=%q", v, "looks fine")
	}

	if !strings.Contains(gotBody, "json_schema") {
		t.Errorf("request body should request structured JSON schema output, got: %s", gotBody)
	}
	if !strings.Contains(gotBody, `"temperature":0`) {
		t.Errorf("request body should set temperature 0, got: %s", gotBody)
	}
	if !strings.Contains(gotBody, "is this plausible") {
		t.Errorf("request body should contain the rubric, got: %s", gotBody)
	}
	if judge.Name() != "test-model" {
		t.Errorf("Name() = %q, want %q", judge.Name(), "test-model")
	}
}

func TestGeminiJudge_NonJSONBodyIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "test",
			"object": "chat.completion",
			"created": 0,
			"model": "test-model",
			"choices": [{
				"index": 0,
				"finish_reason": "stop",
				"message": {"role": "assistant", "content": "I think this suggestion is fine."}
			}]
		}`))
	}))
	defer srv.Close()

	judge, err := NewGeminiJudge(JudgeConfig{APIKey: "test-key", Model: "test-model", BaseURL: srv.URL + "/"})
	if err != nil {
		t.Fatalf("NewGeminiJudge: %v", err)
	}

	if _, err := judge.Judge(context.Background(), JudgeInput{Rubric: "x", Req: req(), Suggestion: "y"}); err == nil {
		t.Fatal("want an error when the endpoint returns prose instead of the structured verdict, got nil")
	}
}

func TestGeminiJudge_HTTPErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error": {"message": "rate limited"}}`))
	}))
	defer srv.Close()

	judge, err := NewGeminiJudge(JudgeConfig{APIKey: "test-key", Model: "test-model", BaseURL: srv.URL + "/"})
	if err != nil {
		t.Fatalf("NewGeminiJudge: %v", err)
	}

	if _, err := judge.Judge(context.Background(), JudgeInput{Rubric: "x", Req: req(), Suggestion: "y"}); err == nil {
		t.Fatal("want an error propagated from a non-2xx response, got nil")
	}
}
