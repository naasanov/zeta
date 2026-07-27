// judge.go is Part 3's LLM judge (plan doc "The judge"): a Judge interface
// backed by Gemini's OpenAI-compatible endpoint, a JudgeGrader that plugs a
// Judge into the ordinary Assertion/Grader seam, and a persistent on-disk
// verdict cache that makes repeat runs affordable (identical suggestions
// across the runner's N samples are judged exactly once).
//
// # Provider blinding (load-bearing)
//
// The judge must never see which provider/model produced the suggestion it
// is grading — self-preference bias is real in the literature even with
// blinding (see the plan doc's "On family bias" section), so the judge
// prompt is built ONLY from JudgeInput's Rubric/Req/Suggestion. Nothing here
// ever threads a provider or model name into the prompt. TestJudgePrompt_Blind
// asserts this by scanning the rendered prompt for known provider/model
// tokens.
//
// # Fail closed, never fail open
//
// A judge that can't produce a clean verdict returns an error, never a
// default Pass/Fail. types.go's GraderErrors/Graded==0 handling already
// treats "never actually graded" as never a silent pass for any polarity;
// this file's job is to make sure a malformed response, an unconfigured
// judge, or a cancelled ctx all take that error path rather than guessing.
package eval

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"

	"github.com/naasanov/zsh-autopilot/daemon/internal/protocol"
)

// ---- Config (env, resolved in one place) ----------------------------------

const (
	envJudgeKey     = "ZSH_AUTOPILOT_EVAL_JUDGE_KEY"
	envJudgeModel   = "ZSH_AUTOPILOT_EVAL_JUDGE_MODEL"
	envJudgeBaseURL = "ZSH_AUTOPILOT_EVAL_JUDGE_BASE_URL"
	envJudgeCache   = "ZSH_AUTOPILOT_EVAL_JUDGE_CACHE"

	// defaultJudgeModel is gemini-3.5-flash-lite (plan doc default): mini-tier
	// judges already clear the >=90% agreement gate, and Flash-Lite is
	// out-of-family against every candidate provider this harness tests
	// (codestral/anthropic/groq), which is where blinding actually matters.
	//
	// NOTE: Gemini model ids use DOTS, not dashes — "gemini-3.5-flash-lite"
	// is correct; a version number rendered with dashes instead of a dot
	// (e.g. "gemini-3" + "-5-flash-lite" run together) looks plausible,
	// parses fine as a flag/env value, and fails at request time with a 404
	// that reads like an auth problem, not a typo'd model id. Verified
	// against the live `/v1beta/openai/models` listing before pinning this
	// default.
	defaultJudgeModel = "gemini-3.5-flash-lite"

	// defaultJudgeBaseURL is Gemini's OpenAI-compatible endpoint, so the judge
	// is reachable through the same openai-go SDK shape as the provider
	// adapters use (by base-URL swap) without routing through
	// internal/provider, which is shaped for suggestion completions, not
	// chat grading.
	defaultJudgeBaseURL = "https://generativelanguage.googleapis.com/v1beta/openai/"
)

// JudgeConfig is the judge's fully-resolved configuration. Build it once via
// JudgeConfigFromEnv and pass it down; nothing else in this package reads
// these env vars directly.
type JudgeConfig struct {
	APIKey   string // required to actually run a judged case
	Model    string
	BaseURL  string
	CacheDir string
}

// JudgeConfigFromEnv resolves JudgeConfig from the environment, applying
// defaults for everything except the API key (which has none — a judge with
// no key is not "defaulted", it's unconfigured).
func JudgeConfigFromEnv() JudgeConfig {
	cfg := JudgeConfig{
		APIKey:   os.Getenv(envJudgeKey),
		Model:    os.Getenv(envJudgeModel),
		BaseURL:  os.Getenv(envJudgeBaseURL),
		CacheDir: os.Getenv(envJudgeCache),
	}
	if cfg.Model == "" {
		cfg.Model = defaultJudgeModel
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultJudgeBaseURL
	}
	if cfg.CacheDir == "" {
		cfg.CacheDir = defaultJudgeCacheDir()
	}
	return cfg
}

// defaultJudgeCacheDir is $XDG_CACHE_HOME/autopilot/eval-judge, falling back
// to ~/.cache/autopilot/eval-judge — the same XDG-with-fallback pattern
// internal/metrics uses for its own default paths.
func defaultJudgeCacheDir() string {
	cacheHome := os.Getenv("XDG_CACHE_HOME")
	if cacheHome == "" {
		home := "."
		if u, err := user.Current(); err == nil && u.HomeDir != "" {
			home = u.HomeDir
		} else if h := os.Getenv("HOME"); h != "" {
			home = h
		}
		cacheHome = filepath.Join(home, ".cache")
	}
	return filepath.Join(cacheHome, "autopilot", "eval-judge")
}

// ---- Judge interface --------------------------------------------------------

// Judge grades one suggestion against a per-case rubric. Implementations
// must never leak which provider/model produced the suggestion into their
// own reasoning inputs beyond what JudgeInput already carries.
type Judge interface {
	Name() string // model id, for the scorecard + cache key
	Judge(ctx context.Context, in JudgeInput) (JudgeVerdict, error)
}

// JudgeInput is everything the judge is allowed to see: the rubric question,
// the request context the completion was generated from, and the full
// suggestion (Req.Buf + the model's output). It deliberately has no
// provider/model field — see the package doc's "Provider blinding" section.
type JudgeInput struct {
	Rubric     string           // the per-case question
	Req        protocol.Request // context the model saw
	Suggestion string           // the full command line: Req.Buf + output
}

// JudgeVerdict is the judge's structured answer.
type JudgeVerdict struct {
	Pass   bool
	Reason string
}

// ---- Gemini (OpenAI-compatible) judge --------------------------------------

// geminiJudge implements Judge via the openai-go SDK pointed at an
// OpenAI-compatible endpoint (Gemini by default, but any such endpoint works
// by construction — same portability property as internal/provider's
// openai.go, just not sharing its code, since that package is shaped for
// suggestion completions rather than chat grading with structured output).
type geminiJudge struct {
	client openai.Client
	model  string
}

// NewGeminiJudge builds a Judge from cfg. It returns an error if cfg.APIKey
// is empty — a judge with no key cannot run, and callers (JudgeGrader via
// the cases.go wiring) must surface that as a clear grader error rather than
// silently skipping or defaulting to a verdict.
func NewGeminiJudge(cfg JudgeConfig) (Judge, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("eval: judge requires %s (see .docs/eval_harness_plan.md, \"The judge\")", envJudgeKey)
	}
	model := cfg.Model
	if model == "" {
		model = defaultJudgeModel
	}
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = defaultJudgeBaseURL
	}
	client := openai.NewClient(
		option.WithBaseURL(baseURL),
		option.WithAPIKey(cfg.APIKey),
	)
	return &geminiJudge{client: client, model: model}, nil
}

func (j *geminiJudge) Name() string { return j.model }

// judgeSystemPrompt is the fixed instruction turn. It states the grading
// contract and the output schema; it must never name a provider/model
// (blinding applies to the judge's OWN identity too, not just the system
// under test's — not that it would matter here, since this string never
// varies by target provider).
const judgeSystemPrompt = `You are grading a single shell command-line suggestion produced by an
autocomplete system. You will be given:
  - a rubric: a short, specific question about the suggestion
  - context: the shell state the suggestion was generated from (current
    directory, git state, exit code, recent command history, directory
    listing)
  - the suggestion: the full resulting command line

Answer ONLY the question the rubric asks — do not grade on style, safety, or
any property the rubric does not mention. Respond with a JSON object:
{"verdict": "pass" or "fail", "reason": a one-sentence explanation}.
"pass" means the property the rubric asks about is PRESENT in the
suggestion; "fail" means it is absent. If the rubric says an empty or
missing suggestion is itself a correct answer, judge it as such rather than
failing it for being empty.`

// judgeResponseSchema is the structured-output JSON schema requested via
// response_format. strict:true asks the endpoint to always conform, but the
// response is still parsed defensively (parseVerdict) since "asked for JSON"
// and "got JSON" are not the same guarantee across every OpenAI-compatible
// endpoint.
var judgeResponseSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"verdict": map[string]any{
			"type": "string",
			"enum": []string{"pass", "fail"},
		},
		"reason": map[string]any{
			"type": "string",
		},
	},
	"required":             []string{"verdict", "reason"},
	"additionalProperties": false,
}

// renderJudgeUser renders the user turn: rubric, context, and suggestion.
// This is the ONLY place request context reaches the judge prompt, and it
// must never include a provider/model field from anywhere else in the
// codebase — see the package doc's "Provider blinding" section and
// TestJudgePrompt_Blind.
func renderJudgeUser(in JudgeInput) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Rubric: %s\n\n", in.Rubric)
	b.WriteString("Context:\n")
	if in.Req.Cwd != "" {
		fmt.Fprintf(&b, "  cwd: %s\n", in.Req.Cwd)
	}
	if in.Req.GitBranch != "" {
		fmt.Fprintf(&b, "  git branch: %s (dirty=%v)\n", in.Req.GitBranch, in.Req.GitDirty)
	}
	if in.Req.LastExit != 0 {
		fmt.Fprintf(&b, "  last exit code: %d\n", in.Req.LastExit)
	}
	if len(in.Req.DirEntries) > 0 {
		fmt.Fprintf(&b, "  directory entries: %s\n", strings.Join(in.Req.DirEntries, " "))
	}
	if len(in.Req.History) > 0 {
		b.WriteString("  recent command history (oldest first):\n")
		for _, h := range in.Req.History {
			fmt.Fprintf(&b, "    %s\n", h)
		}
	}
	if in.Req.Buf != "" {
		fmt.Fprintf(&b, "  buffer already typed: %q\n", in.Req.Buf)
	}
	fmt.Fprintf(&b, "\nSuggestion (full resulting command line): %q\n", in.Suggestion)
	return b.String()
}

// Judge sends the rubric+context+suggestion to the configured
// OpenAI-compatible endpoint at temperature 0 (the judge is measurement
// apparatus — stability is what we want from it, unlike the systems under
// test where production settings are deliberately used unchanged) and
// requests the structured verdict schema.
func (j *geminiJudge) Judge(ctx context.Context, in JudgeInput) (JudgeVerdict, error) {
	params := openai.ChatCompletionNewParams{
		Model: j.model,
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(judgeSystemPrompt),
			openai.UserMessage(renderJudgeUser(in)),
		},
		Temperature: openai.Float(0),
		ResponseFormat: openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONSchema: &shared.ResponseFormatJSONSchemaParam{
				JSONSchema: shared.ResponseFormatJSONSchemaJSONSchemaParam{
					Name:   "verdict",
					Strict: openai.Bool(true),
					Schema: judgeResponseSchema,
				},
			},
		},
	}

	resp, err := j.client.Chat.Completions.New(ctx, params)
	if err != nil {
		if ctx.Err() != nil {
			return JudgeVerdict{}, fmt.Errorf("eval: judge: %w", ctx.Err())
		}
		return JudgeVerdict{}, fmt.Errorf("eval: judge request: %w", err)
	}
	if len(resp.Choices) == 0 {
		return JudgeVerdict{}, errors.New("eval: judge: response had no choices")
	}
	return parseVerdict(resp.Choices[0].Message.Content)
}

// judgeResponse is the wire shape parseVerdict expects. Fields are required:
// a missing verdict/reason is a parse failure, not a default.
type judgeResponse struct {
	Verdict string `json:"verdict"`
	Reason  string `json:"reason"`
}

// parseVerdict parses the judge's raw text content defensively. Any
// deviation from the exact expected shape — prose, malformed JSON, a missing
// field, an unrecognized verdict value — is an ERROR, never a default
// verdict. See the package doc's "Fail closed" note for why: a judge that
// fails open would silently mark everything passing, exactly the failure
// mode the harness's Graded==0 handling exists to catch, but only if this
// function actually returns an error instead of masking it.
func parseVerdict(raw string) (JudgeVerdict, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return JudgeVerdict{}, errors.New("eval: judge: empty response")
	}

	var resp judgeResponse
	dec := json.NewDecoder(bytes.NewReader([]byte(trimmed)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&resp); err != nil {
		return JudgeVerdict{}, fmt.Errorf("eval: judge: response is not valid JSON: %w", err)
	}
	// A JSON decoder happily stops after the first value even if trailing
	// garbage follows (e.g. prose appended after a JSON blob) — reject that
	// explicitly rather than silently accepting a partial parse.
	if dec.More() {
		return JudgeVerdict{}, errors.New("eval: judge: trailing content after JSON value")
	}

	switch resp.Verdict {
	case "pass":
		if resp.Reason == "" {
			return JudgeVerdict{}, errors.New("eval: judge: missing reason field")
		}
		return JudgeVerdict{Pass: true, Reason: resp.Reason}, nil
	case "fail":
		if resp.Reason == "" {
			return JudgeVerdict{}, errors.New("eval: judge: missing reason field")
		}
		return JudgeVerdict{Pass: false, Reason: resp.Reason}, nil
	case "":
		return JudgeVerdict{}, errors.New("eval: judge: missing verdict field")
	default:
		return JudgeVerdict{}, fmt.Errorf("eval: judge: unrecognized verdict %q", resp.Verdict)
	}
}

// ---- Persistent verdict cache ----------------------------------------------

// JudgeCache is a persistent, on-disk verdict cache keyed by
// hash(caseID, rubric, judgeModel, suggestion) — see cacheKey. It is what
// makes repeat runs affordable: identical suggestions across a case's N
// samples, or across successive full runs, cost exactly one judge call.
//
// One file per key, JSON-encoded, named by the key's own hex digest. A
// corrupt or unreadable entry is treated as a cache miss, never a fatal
// error (Get swallows read/unmarshal errors on purpose).
type JudgeCache struct {
	dir    string
	bypass bool // Get always misses when true; Set still writes (a "refresh" run)
}

// NewJudgeCache builds a cache rooted at dir. bypass forces every Get to
// miss (so every case is re-judged against the live model) while Set keeps
// writing, so a bypass run also refreshes the cache for subsequent normal
// runs.
func NewJudgeCache(dir string, bypass bool) *JudgeCache {
	return &JudgeCache{dir: dir, bypass: bypass}
}

// cacheKey hashes the components the plan doc specifies: caseID, the rubric
// text (a rubric change is a version change — editing a rubric must
// invalidate its cached verdicts, and hashing the text itself achieves that
// without a separately-tracked version counter to keep in sync), the judge's
// model id, and the suggestion being graded.
func cacheKey(caseID, rubric, judgeModel, suggestion string) string {
	h := sha256.New()
	for _, part := range []string{caseID, rubric, judgeModel, suggestion} {
		h.Write([]byte(part))
		h.Write([]byte{0}) // separator so ("ab","c") != ("a","bc")
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (c *JudgeCache) path(key string) string {
	return filepath.Join(c.dir, key+".json")
}

// Get returns the cached verdict for key, if any. A missing file, an
// unreadable file, or a file that fails to unmarshal are all reported as a
// plain miss (ok == false) — never an error, per the plan doc's "a corrupt
// or unreadable cache entry must be treated as a miss, never a fatal error".
func (c *JudgeCache) Get(key string) (JudgeVerdict, bool) {
	if c == nil || c.bypass {
		return JudgeVerdict{}, false
	}
	data, err := os.ReadFile(c.path(key))
	if err != nil {
		return JudgeVerdict{}, false
	}
	var v JudgeVerdict
	if err := json.Unmarshal(data, &v); err != nil {
		return JudgeVerdict{}, false
	}
	return v, true
}

// Set persists v under key. Write failures (e.g. an unwritable cache dir)
// are swallowed: caching is an optimization, and a cache that can't write
// today must not fail the eval run that triggered it — the case still got
// judged, it just won't be free next time.
func (c *JudgeCache) Set(key string, v JudgeVerdict) {
	if c == nil {
		return
	}
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return
	}
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	_ = os.WriteFile(c.path(key), data, 0o644)
}

// ---- JudgeGrader: the Judge -> Grader adapter ------------------------------

// judgeGrader adapts a Judge (plus an optional cache) to the Grader
// interface, so a judged case is an ordinary Assertion like any other.
// Semantics match every other grader in this package: it reports PRESENCE of
// the shape the rubric describes, never polarity — the Assertion.Polarity
// (Must, per the plan doc's judged cases) decides whether presence is good.
type judgeGrader struct {
	caseID string
	label  string
	rubric string
	judge  Judge // nil means "unconfigured" — see unconfiguredErr
	cache  *JudgeCache

	// unconfiguredErr, when set, makes Grade return it immediately without
	// touching judge/cache. This is how an unconfigured judge (no API key in
	// the environment) surfaces: a clear grader error on every sample, which
	// types.go's Graded==0 handling already renders as a non-pass for every
	// polarity — never a panic, never a silent pass. See cases.go's
	// judgedGrader helper, the single place this gets constructed either way.
	unconfiguredErr error
}

// NewJudgeGrader builds a Grader that calls judge (through cache, if
// non-nil) to answer rubric for the given caseID. label names the assertion
// for reporting, matching every other grader's Name() convention.
func NewJudgeGrader(caseID, label, rubric string, judge Judge, cache *JudgeCache) Grader {
	return &judgeGrader{caseID: caseID, label: label, rubric: rubric, judge: judge, cache: cache}
}

// NewUnconfiguredJudgeGrader builds a Grader that always fails with err —
// used when no judge could be constructed (e.g. missing API key) so the
// case still has a well-formed Grader (required by TestCases_WellFormed)
// that fails loudly rather than a nil Grader that would panic the runner.
func NewUnconfiguredJudgeGrader(caseID, label string, err error) Grader {
	return &judgeGrader{caseID: caseID, label: label, unconfiguredErr: err}
}

// Name is stable across process runs regardless of whether a judge was
// actually configured (TestCases_Deterministic compares Grader.Name() across
// two Cases() calls within the same process/environment, so this must not
// vary with e.g. a pointer address).
func (g *judgeGrader) Name() string {
	return "judge:" + g.caseID + ":" + g.label
}

// Grade forwards ctx to the underlying Judge (a cancelled ctx must surface
// as an error here, not a verdict — the CtxGraderFunc/AnyOf/AllOf combinators
// exist specifically so a judge nested anywhere in the assertion tree stays
// cancellable; this is the leaf that must actually honor it).
func (g *judgeGrader) Grade(ctx context.Context, in protocol.Request, out string) (bool, error) {
	if g.unconfiguredErr != nil {
		return false, g.unconfiguredErr
	}
	if g.judge == nil {
		return false, errors.New("eval: judge grader has no judge configured")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}

	suggestion := fullCommand(in, out)
	key := cacheKey(g.caseID, g.rubric, g.judge.Name(), suggestion)

	if v, ok := g.cache.Get(key); ok {
		return v.Pass, nil
	}

	verdict, err := g.judge.Judge(ctx, JudgeInput{
		Rubric:     g.rubric,
		Req:        in,
		Suggestion: suggestion,
	})
	if err != nil {
		return false, err
	}

	g.cache.Set(key, verdict)
	return verdict.Pass, nil
}

// defaultJudgeGrader is the single call site that resolves the judge and its
// cache from the environment (JudgeConfigFromEnv) and builds the Grader for
// a judged case in cases.go. It never panics and never returns a nil Grader:
// with no API key configured (the common case in -dry-run and in every unit
// test), it returns a Grader that fails loudly and identically on every
// sample — Graded stays 0 for that assertion, which report.go already
// renders as a flagged, non-passing result rather than a silent pass. This
// is the deliberate choice for "how does an unconfigured judge behave in
// dry-run": a clean, always-the-same grader error, not a panic.
func defaultJudgeGrader(caseID, label, rubric string) Grader {
	cfg := JudgeConfigFromEnv()
	judge, err := NewGeminiJudge(cfg)
	if err != nil {
		return NewUnconfiguredJudgeGrader(caseID, label, err)
	}
	cache := NewJudgeCache(cfg.CacheDir, false)
	return NewJudgeGrader(caseID, label, rubric, judge, cache)
}
