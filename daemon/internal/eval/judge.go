// Package eval implements the LLM judge, including its verdict cache.
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
	"strconv"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"

	"github.com/naasanov/zsh-autopilot/daemon/internal/protocol"
)

// ---- Config (env, resolved in one place) ----------------------------------

const (
	envJudgeKey        = "ZSH_AUTOPILOT_EVAL_JUDGE_KEY"
	envJudgeModel      = "ZSH_AUTOPILOT_EVAL_JUDGE_MODEL"
	envJudgeBaseURL    = "ZSH_AUTOPILOT_EVAL_JUDGE_BASE_URL"
	envJudgeCache      = "ZSH_AUTOPILOT_EVAL_JUDGE_CACHE"
	envJudgeRatePerMin = "ZSH_AUTOPILOT_EVAL_JUDGE_RATE_PER_MIN"

	defaultJudgeRatePerMin = 12

	// defaultJudgeModel: Gemini model ids use dots, not dashes.
	// A dashed variant is valid syntax but 404s at request time.
	defaultJudgeModel = "gemini-3.5-flash-lite"

	defaultJudgeBaseURL = "https://generativelanguage.googleapis.com/v1beta/openai/"
)

// judgeRetryBackoff is fixed: Gemini doesn't reliably set a retry-after header.
var judgeRetryBackoff = 2 * time.Second

// JudgeConfig is the judge's resolved configuration.
type JudgeConfig struct {
	APIKey     string
	Model      string
	BaseURL    string
	CacheDir   string
	RatePerMin int // calls/minute; <=0 resolves to defaultJudgeRatePerMin
}

// JudgeConfigFromEnv defaults every field except APIKey, which stays empty
// to signal "unconfigured" rather than a valid value.
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
	cfg.RatePerMin = defaultJudgeRatePerMin
	if raw := os.Getenv(envJudgeRatePerMin); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			cfg.RatePerMin = n
		}
	}
	return cfg
}

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

type Judge interface {
	Name() string
	Judge(ctx context.Context, in JudgeInput) (JudgeVerdict, error)
}

// JudgeInput excludes a provider/model field; judges must stay blind to it.
type JudgeInput struct {
	Rubric     string
	Req        protocol.Request
	Suggestion string // the full command line: Req.Buf + output
}

type JudgeVerdict struct {
	Pass   bool
	Reason string
}

// ---- Gemini (OpenAI-compatible) judge --------------------------------------

type geminiJudge struct {
	client  openai.Client
	model   string
	limiter Limiter // never nil
}

// NewGeminiJudge errors if cfg.APIKey is empty.
func NewGeminiJudge(cfg JudgeConfig) (Judge, error) {
	ratePerMin := cfg.RatePerMin
	if ratePerMin <= 0 {
		ratePerMin = defaultJudgeRatePerMin
	}
	j, err := newGeminiJudge(cfg, NewRateLimiter(ratePerMin))
	if err != nil {
		return nil, err
	}
	return j, nil
}

func newGeminiJudge(cfg JudgeConfig, limiter Limiter) (*geminiJudge, error) {
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
	if limiter == nil {
		limiter = NoopLimiter{}
	}
	// WithMaxRetries(0): the SDK's own retries would hide 429s from the
	// retry-once logic below.
	client := openai.NewClient(
		option.WithBaseURL(baseURL),
		option.WithAPIKey(cfg.APIKey),
		option.WithMaxRetries(0),
	)
	return &geminiJudge{client: client, model: model, limiter: limiter}, nil
}

func (j *geminiJudge) Name() string { return j.model }

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

// Judge retries a 429 once after a backoff; a second failure surfaces as an error.
func (j *geminiJudge) Judge(ctx context.Context, in JudgeInput) (JudgeVerdict, error) {
	if err := j.limiter.Wait(ctx); err != nil {
		return JudgeVerdict{}, fmt.Errorf("eval: judge: %w", err)
	}

	resp, err := j.complete(ctx, in)
	if err != nil && isRateLimited(err) {
		if backoffErr := sleepOrDone(ctx, judgeRetryBackoff); backoffErr != nil {
			return JudgeVerdict{}, fmt.Errorf("eval: judge: %w", backoffErr)
		}
		if err := j.limiter.Wait(ctx); err != nil {
			return JudgeVerdict{}, fmt.Errorf("eval: judge: %w", err)
		}
		resp, err = j.complete(ctx, in)
	}
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

func (j *geminiJudge) complete(ctx context.Context, in JudgeInput) (*openai.ChatCompletion, error) {
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
	return j.client.Chat.Completions.New(ctx, params)
}

func isRateLimited(err error) bool {
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == 429
	}
	return false
}

func sleepOrDone(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type judgeResponse struct {
	Verdict string `json:"verdict"`
	Reason  string `json:"reason"`
}

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
	// dec.More() catches trailing content after the first JSON value,
	// which Decode alone ignores.
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

type JudgeCache struct {
	dir    string
	bypass bool // Get always misses when true; Set still writes
}

func NewJudgeCache(dir string, bypass bool) *JudgeCache {
	return &JudgeCache{dir: dir, bypass: bypass}
}

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

type judgeGrader struct {
	caseID string
	label  string
	rubric string
	judge  Judge // nil means unconfigured
	cache  *JudgeCache

	unconfiguredErr error
}

func NewJudgeGrader(caseID, label, rubric string, judge Judge, cache *JudgeCache) Grader {
	return &judgeGrader{caseID: caseID, label: label, rubric: rubric, judge: judge, cache: cache}
}

func NewUnconfiguredJudgeGrader(caseID, label string, err error) Grader {
	return &judgeGrader{caseID: caseID, label: label, unconfiguredErr: err}
}

// Name must stay stable across instances of the same case/label.
func (g *judgeGrader) Name() string {
	return "judge:" + g.caseID + ":" + g.label
}

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

func defaultJudgeGrader(caseID, label, rubric string) Grader {
	cfg := JudgeConfigFromEnv()
	judge, err := NewGeminiJudge(cfg)
	if err != nil {
		return NewUnconfiguredJudgeGrader(caseID, label, err)
	}
	cache := NewJudgeCache(cfg.CacheDir, false)
	return NewJudgeGrader(caseID, label, rubric, judge, cache)
}
