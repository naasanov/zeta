// Command eval is the live-provider suggestion-quality evaluation harness
// (see .docs/eval_harness_plan.md). It is a `main` package, not a test,
// precisely so it never runs under `go test ./...`: evals are slow, cost
// money, hit live rate limits, and are non-deterministic by construction.
// `go build ./...` still compile-checks it.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/naasanov/zsh-autopilot/daemon/internal/config"
	"github.com/naasanov/zsh-autopilot/daemon/internal/eval"
	"github.com/naasanov/zsh-autopilot/daemon/internal/prompt"
	"github.com/naasanov/zsh-autopilot/daemon/internal/provider"
)

// maxN is the hard cap on -n: adaptive sampling already escalates to at most
// this many runs, so a fixed-N override above it wastes calls for no more
// signal. Also stands in for the adaptive ceiling when estimating a
// pre-flight worst-case call count below.
const maxN = 10

// matrixProviders is what `-matrix` expands -providers to. ollama (untested
// local placeholder model) and the openai escape hatch (no sensible default
// base_url/model) are excluded from an unattended full-matrix run.
var matrixProviders = []string{"codestral", "anthropic", "groq"}

func main() {
	var (
		n             = flag.Int("n", 0, "fixed run count per case (0 = adaptive, hard-capped at 10)")
		caseSel       = flag.String("cases", "", "comma-separated case selectors: IDs (\"A1,B2\"), categories (\"syntax\"), or ID globs (\"A*\"); empty runs everything")
		outPath       = flag.String("out", "", "write the JSON report to this path (in addition to the text scorecard on stdout); if it's an existing directory, a timestamped provider/variant-named file is created inside it")
		printJSON     = flag.Bool("print-json", false, "print the JSON report to stdout before the scorecard, with embedded newlines (e.g. in the rendered Prompt field) shown literally rather than escaped — NOT valid JSON, a terminal-reading convenience only")
		dryRun        = flag.Bool("dry-run", false, "use a scripted stub provider instead of a live one (no network); -providers/-matrix are ignored, -prompts still selects the prompt-building path")
		providersFlag = flag.String("providers", "codestral", "comma-separated provider brands to run against (\"codestral,anthropic,groq\"); the cheap default is a single provider")
		promptsFlag   = flag.String("prompts", "default", "comma-separated registered prompt names to run (\"fim-transcript-marker,chat-append\"); \"default\" is an input alias that resolves per-provider to prompt.ShippedFor(adapter) — never persisted as-is")
		matrix        = flag.Bool("matrix", false, "shorthand for every provider (matrixProviders) x every registered prompt; the occasional full run, not the default")
		modelOverride = flag.String("model", "", "override the resolved model for every selected provider (see newLiveProvider); empty keeps each provider's preset default")
		importPath    = flag.String("import", "", "read a §12 metrics events.jsonl, print case stubs + import stats to stdout/stderr, and exit without running anything")
		diffMode      = flag.Bool("diff", false, "compare two scorecard JSON dumps (positional args: old.json new.json), print the diff, and exit non-zero if anything regressed; a terminal mode like -import — never runs cases")
		judgeValidate = flag.Bool("judge-validate", false, "score candidate judge models (-judges) against hand labels (-labels) and exit non-zero unless the best clears the plan doc's >=90% agreement gate; a terminal mode like -diff/-import — never runs cases")
		judgesFlag    = flag.String("judges", "", "comma-separated judge model ids to validate (\"gemini-3.5-flash-lite,gpt-5-mini\"); empty defaults to the single configured judge model (ZSH_AUTOPILOT_EVAL_JUDGE_MODEL or its default)")
		labelsPath    = flag.String("labels", eval.DefaultJudgeLabelsPath, "path to the hand-labeled judge_labels.jsonl (see .docs/eval_harness_plan.md, \"The judge\")")
	)
	flag.Parse()

	// setFlags is which flags were actually passed on the command line (as
	// opposed to left at their default), so the terminal modes below (-diff,
	// -import) can detect run-only flags that would otherwise be silently
	// ignored rather than quietly dropping them — see runOnlyFlagNames.
	setFlags := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { setFlags[f.Name] = true })

	terminalModesSet := 0
	for _, set := range []bool{*diffMode, *importPath != "", *judgeValidate} {
		if set {
			terminalModesSet++
		}
	}
	if terminalModesSet > 1 {
		log.Fatalf("eval: -diff, -import, and -judge-validate are mutually exclusive terminal modes; run one at a time")
	}

	if *diffMode {
		if ignored := setRunOnlyFlags(setFlags); len(ignored) > 0 {
			log.Fatalf("eval: -diff never runs cases, so %s would be silently ignored; drop them", strings.Join(ignored, ", "))
		}
		args := flag.Args()
		if len(args) != 2 {
			log.Fatalf("eval: -diff requires exactly two positional args (old.json new.json), got %d: %v", len(args), args)
		}
		runDiff(args[0], args[1])
		return
	}

	if *importPath != "" {
		if ignored := setRunOnlyFlags(setFlags); len(ignored) > 0 {
			log.Fatalf("eval: -import never runs cases, so %s would be silently ignored; drop them", strings.Join(ignored, ", "))
		}
		runImport(*importPath)
		return
	}

	if *judgeValidate {
		if ignored := setRunOnlyFlags(setFlags); len(ignored) > 0 {
			log.Fatalf("eval: -judge-validate never runs cases, so %s would be silently ignored; drop them", strings.Join(ignored, ", "))
		}
		runJudgeValidate(*judgesFlag, *labelsPath)
		return
	}

	if *n > maxN {
		log.Fatalf("eval: -n=%d exceeds the hard cap of %d; the plan doc treats anything higher as wasted "+
			"budget over what adaptive sampling already reaches (see .docs/eval_harness_plan.md, "+
			"\"No temperature knob\"/guardrail note)", *n, maxN)
	}
	if *n < 0 {
		log.Fatalf("eval: -n=%d is invalid; use 0 for adaptive sampling", *n)
	}

	cases, err := eval.Select(eval.Cases(), *caseSel)
	if err != nil {
		// Fatal rather than "run what matched": a selector typo that quietly
		// narrows the run exits 0 with a clean scorecard for cases that never
		// executed. See eval.Select's doc comment.
		log.Fatalf("eval: -cases=%q: %v", *caseSel, err)
	}

	cells, skipped := resolveCells(*dryRun, *matrix, *providersFlag, *promptsFlag)
	for _, s := range skipped {
		fmt.Fprintf(os.Stderr, "eval: skipping incompatible pairing %s\n", s)
	}

	perCaseMax := maxN
	nPolicy := "adaptive(min=3,max=10)"
	if *n > 0 {
		perCaseMax = *n
		nPolicy = fmt.Sprintf("fixed=%d", *n)
	}
	maxCalls := len(cells) * len(cases) * perCaseMax
	fmt.Fprintf(os.Stderr, "eval: %d cell(s) (%d provider(s) x %d prompt(s)) x %d case(s), up to %d run(s)/case => up to %d total provider calls\n",
		len(cells), countDistinctProviders(cells), countDistinctPrompts(cells), len(cases), perCaseMax, maxCalls)

	fmt.Fprintf(os.Stderr, "eval: legend  . pass   x fail   ! trip-wire tripped   E no successful runs\n")

	start := time.Now()

	// rowLabel width, so multi-cell runs line their dots up in a column
	// instead of ragged-right.
	labelWidth := 0
	for _, c := range cells {
		if w := len(cellLabel(c)); w > labelWidth {
			labelWidth = w
		}
	}

	var allResults []eval.CaseResult
	var lastMeta eval.Meta
	for _, c := range cells {
		p, limiter, err := buildCellProvider(c, *dryRun, *modelOverride)
		if err != nil {
			// Fatal, not "skip this cell and keep going": a provider that
			// silently dropped out of a matrix run would produce a report
			// that looks like a smaller-but-clean matrix rather than a
			// broken one. See newLiveProvider's doc comment on why a
			// missing key must never fall back to a stub.
			log.Fatalf("eval: provider %q: %v", c.Provider, err)
		}

		// One row per cell, symbols streaming out as cases finish. Progress
		// goes to stderr so a piped or -out'd stdout stays a clean scorecard.
		fmt.Fprintf(os.Stderr, "%-*s  ", labelWidth, cellLabel(c))
		var passed, failed, tripped, errored int
		runner := &eval.Runner{
			Provider: p, Limiter: limiter, FixedN: *n,
			// A shared rate limiter makes extra workers pure queuing latency
			// (see Runner.Concurrency's doc comment) — run those cells
			// single-worker so a cell's paced-out slots all go to whichever
			// case is next in report order instead of scattering across
			// several concurrent cases.
			Concurrency: concurrencyFor(limiter),
			// The BRAND, not p.Name() (the adapter) — see Runner.ProviderLabel.
			ProviderLabel: c.Provider,
			Progress: func(_ eval.Case, r eval.CaseResult) {
				sym := eval.CaseSymbol(r)
				switch sym {
				case '.':
					passed++
				case 'x':
					failed++
				case '!':
					tripped++
				case 'E':
					errored++
				}
				fmt.Fprint(os.Stderr, eval.ColorizeSymbol(sym))
			},
		}
		results := runner.Run(context.Background(), cases)
		passedLabel := fmt.Sprintf("%d/%d passed", passed, len(cases))
		if passed == len(cases) {
			passedLabel = eval.ColorGood(passedLabel)
		}
		fmt.Fprintf(os.Stderr, "  %s", passedLabel)
		if tripped > 0 {
			fmt.Fprintf(os.Stderr, ", %s", eval.ColorBad(fmt.Sprintf("%d TRIPPED", tripped)))
		}
		if failed > 0 {
			fmt.Fprintf(os.Stderr, ", %s", eval.ColorBad(fmt.Sprintf("%d failed", failed)))
		}
		if errored > 0 {
			fmt.Fprintf(os.Stderr, ", %s", eval.ColorWarn(fmt.Sprintf("%d errored", errored)))
		}
		fmt.Fprintln(os.Stderr)
		allResults = append(allResults, results...)

		lastMeta = eval.Meta{
			Provider:  c.Provider, // brand, matching the scorecard's column labels
			Model:     p.Model(),
			Prompt:    c.PromptName,
			NPolicy:   nPolicy,
			Timestamp: start,
		}
	}

	meta := lastMeta
	if len(cells) > 1 {
		meta = combinedMeta(cells, nPolicy, start)
	}

	elapsed := time.Since(start)

	// Printed before the scorecard, so a terminal reads (top to bottom, in
	// scroll order) live progress -> this detailed JSON dump -> the
	// scorecard -> the summary footer: the more detailed a section, the
	// further back it scrolls, so the "at a glance" verdict is always the
	// last thing on screen.
	if *printJSON {
		if err := eval.PrettyJSON(os.Stdout, allResults, meta); err != nil {
			log.Fatalf("eval: printing JSON report: %v", err)
		}
		fmt.Fprintln(os.Stdout)
	}

	if err := eval.Text(os.Stdout, allResults, meta); err != nil {
		log.Fatalf("eval: writing text report: %v", err)
	}
	fmt.Fprintf(os.Stderr, "eval: done in %s\n", elapsed.Round(time.Millisecond))

	if *outPath != "" {
		path := *outPath
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			path = filepath.Join(path, defaultOutFilename(meta))
		}
		f, err := os.Create(path)
		if err != nil {
			log.Fatalf("eval: creating -out file: %v", err)
		}
		defer f.Close()
		if err := eval.JSON(f, allResults, meta); err != nil {
			log.Fatalf("eval: writing JSON report: %v", err)
		}
	}
}

// defaultOutFilename builds the -out filename when it's pointed at a
// directory: a timestamp prefix (alpha-sorts chronologically) plus the
// provider x variant combo, so `ls` alone tells you what's inside.
func defaultOutFilename(meta eval.Meta) string {
	return fmt.Sprintf("%s_%s_%s.json",
		meta.Timestamp.Format("20060102-150405"),
		sanitizeForFilename(meta.Provider),
		sanitizeForFilename(meta.Prompt))
}

// sanitizeForFilename turns a comma-joined meta field (e.g.
// "codestral,anthropic") into a filename-safe segment ("codestral+anthropic").
func sanitizeForFilename(s string) string {
	return strings.ReplaceAll(s, ",", "+")
}

// cell is one (provider brand, resolved prompt name) combination — one row
// of the matrix this command assembles from -providers/-prompts/-matrix.
// PromptName is always a concrete registered name; "default" never survives
// resolveCells.
type cell struct {
	Provider   string // brand name; "stub" under -dry-run
	PromptName string
}

// cellLabel is the row prefix for a cell's live progress line, e.g.
// "codestral/fim-transcript-marker".
func cellLabel(c cell) string { return c.Provider + "/" + c.PromptName }

// dryRunAdapter is the adapter "default" resolves against under -dry-run,
// where there is no real provider axis to key it to. Chosen to match the
// shipped default provider (codestral/FIM), so a plain `-dry-run` smoke-tests
// the same prompt shape a real default run would use.
const dryRunAdapter = "codestral"

// resolvePromptName resolves one -prompts entry to a concrete registered
// prompt.Prompt. "default" is an INPUT ALIAS ONLY: it resolves per-adapter to
// prompt.ShippedFor(adapter), and the returned prompt's own Name() — never
// the literal string "default" — is what a caller may persist. Any other
// name is validated via prompt.ByName, fatal on a typo (its error lists every
// valid name).
func resolvePromptName(name, adapter string) prompt.Prompt {
	if name == "default" {
		return prompt.ShippedFor(adapter)
	}
	p, err := prompt.ByName(name)
	if err != nil {
		log.Fatalf("eval: -prompts: %v", err)
	}
	return p
}

// promptShape reports which rendering shape p implements, for compatibility
// checks and error messages.
func promptShape(p prompt.Prompt) string {
	_, chat := p.(prompt.ChatPrompt)
	_, fim := p.(prompt.FIMPrompt)
	switch {
	case chat && fim:
		return "chat+FIM"
	case fim:
		return "FIM"
	default:
		return "chat"
	}
}

// shapeNeededBy returns the prompt shape adapter requires: "chat" for
// openai/anthropic, "FIM" for codestral.
func shapeNeededBy(adapter string) string {
	if adapter == "codestral" {
		return "FIM"
	}
	return "chat"
}

// promptCompatible reports whether p can render for a provider needing
// shape.
func promptCompatible(p prompt.Prompt, shape string) bool {
	switch shape {
	case "FIM":
		_, ok := p.(prompt.FIMPrompt)
		return ok
	default:
		_, ok := p.(prompt.ChatPrompt)
		return ok
	}
}

// promptNamesWithShape lists every registered prompt.All() name matching
// shape, for "here's what would work" error text.
func promptNamesWithShape(shape string) []string {
	var out []string
	for _, p := range prompt.All() {
		if promptCompatible(p, shape) {
			out = append(out, p.Name())
		}
	}
	return out
}

// resolveCells assembles the -providers x -prompts matrix and applies the
// shape-compatibility rule: an incompatible (provider, prompt) pairing is
// SKIPPED (returned separately for the caller to report), but a requested
// provider or prompt left with zero resulting cells is FATAL — same
// principle as a -cases selector matching nothing: a silently narrowed
// matrix must never be presentable as a clean one. Runs entirely before any
// provider is constructed. Under -dry-run, -providers/-matrix are ignored
// entirely (a single scripted stub, no provider axis, no incompatibility to
// check), but -prompts still selects which prompt-building path the stub
// exercises.
func resolveCells(dryRun, matrix bool, providersFlag, promptsFlag string) ([]cell, []string) {
	promptNames := splitCSV(promptsFlag)
	if matrix {
		promptNames = nil
		for _, p := range prompt.All() {
			promptNames = appendUnique(promptNames, p.Name())
		}
	}

	if dryRun {
		cells := make([]cell, 0, len(promptNames))
		for _, name := range promptNames {
			p := resolvePromptName(name, dryRunAdapter)
			cells = append(cells, cell{Provider: "stub", PromptName: p.Name()})
		}
		return cells, nil
	}

	providerNames := splitCSV(providersFlag)
	if matrix {
		providerNames = matrixProviders
	}

	var cfg config.Config
	adapterFor := make(map[string]string, len(providerNames))
	for _, pn := range providerNames {
		resolved, err := cfg.Resolve(pn)
		if err != nil {
			log.Fatalf("eval: -providers=%q: %v", providersFlag, err)
		}
		adapterFor[pn] = resolved.Adapter
	}

	var cells []cell
	var skipped []string
	providerSeen := make(map[string]bool, len(providerNames))
	promptSeen := make(map[string]bool, len(promptNames))
	for _, pn := range providerNames {
		shape := shapeNeededBy(adapterFor[pn])
		for _, name := range promptNames {
			p := resolvePromptName(name, adapterFor[pn])
			if !promptCompatible(p, shape) {
				skipped = append(skipped, fmt.Sprintf("%s/%s (provider needs %s, prompt is %s-shaped)",
					pn, p.Name(), shape, promptShape(p)))
				continue
			}
			cells = append(cells, cell{Provider: pn, PromptName: p.Name()})
			providerSeen[pn] = true
			promptSeen[name] = true
		}
	}

	var errs []string
	for _, pn := range providerNames {
		if providerSeen[pn] {
			continue
		}
		shape := shapeNeededBy(adapterFor[pn])
		errs = append(errs, fmt.Sprintf("provider %q needs a %s prompt, but none of -prompts=%q is %s-shaped (registered %s prompts: %s)",
			pn, shape, promptsFlag, shape, shape, strings.Join(promptNamesWithShape(shape), ", ")))
	}
	for _, name := range promptNames {
		if promptSeen[name] {
			continue
		}
		// "default" resolves per-adapter to a prompt already shaped for that
		// adapter, so it can only land here as a literal, incompatible name.
		p, _ := prompt.ByName(name)
		shape := promptShape(p)
		errs = append(errs, fmt.Sprintf("prompt %q is %s-shaped, but none of -providers=%q needs that shape (registered providers in this run: %s)",
			name, shape, providersFlag, strings.Join(providerNames, ", ")))
	}
	if len(errs) > 0 {
		log.Fatalf("eval: -providers/-prompts produced zero cells for:\n  %s", strings.Join(errs, "\n  "))
	}

	return cells, skipped
}

// buildCellProvider constructs the provider.Provider and eval.Limiter for one
// matrix cell. Under -dry-run it always returns a freshly scripted
// StubProvider with a NoopLimiter, regardless of c.Provider. c.PromptName is
// always a concrete registered name by the time it reaches here.
func buildCellProvider(c cell, dryRun bool, modelOverride string) (provider.Provider, eval.Limiter, error) {
	if dryRun {
		return eval.NewStubProvider(c.PromptName,
			eval.StubResult{Output: " status"},
			eval.StubResult{Output: " status"},
			eval.StubResult{Output: " status"},
		), eval.NoopLimiter{}, nil
	}

	p, err := prompt.ByName(c.PromptName)
	if err != nil {
		return nil, nil, err
	}
	prov, err := newLiveProvider(c.Provider, modelOverride, config.DefaultMaxTokens, p)
	if err != nil {
		return nil, nil, err
	}
	return prov, eval.LimiterForBrand(c.Provider), nil
}

// newLiveProvider resolves brand ("codestral"/"anthropic"/"groq"/"ollama", or
// the "openai" escape hatch) into a real provider.Provider, using an empty
// config.Config so only the preset table applies. modelOverride, if
// non-empty, replaces the preset's default model.
//
// A missing required API key is a clear, named, fatal error — never a
// silent fallback to echo/stub output, unlike cmd/autopilotd's degrade path:
// an eval that quietly measured a stub would produce numbers that look real
// and aren't.
func newLiveProvider(brand string, modelOverride string, maxTokens int, p prompt.Prompt) (provider.Provider, error) {
	var cfg config.Config
	resolved, err := cfg.Resolve(brand)
	if err != nil {
		return nil, fmt.Errorf("eval: resolving provider %q: %w", brand, err)
	}
	if modelOverride != "" {
		resolved.Model = modelOverride
	}

	apiKey, err := resolved.ResolveKey()
	if err != nil {
		return nil, fmt.Errorf("eval: resolving API key for provider %q: %w", brand, err)
	}
	if resolved.NeedsKey() && apiKey == "" {
		return nil, fmt.Errorf("eval: provider %q needs an API key; set %s (or configure api_key_cmd)", brand, resolved.APIKeyEnv)
	}

	prov, err := provider.NewFromProfile(resolved, apiKey, maxTokens, p)
	if err != nil {
		return nil, fmt.Errorf("eval: constructing provider %q: %w", brand, err)
	}
	return prov, nil
}

// concurrencyFor returns the Runner.Concurrency to use for a cell's limiter:
// 1 for a real *eval.RateLimiter (spreading a shared per-minute budget across
// workers only adds queuing latency, never throughput), 0 (Runner's default)
// otherwise.
func concurrencyFor(limiter eval.Limiter) int {
	if _, ok := limiter.(*eval.RateLimiter); ok {
		return 1
	}
	return 0
}

// combinedMeta builds the report Meta for a multi-cell run: Provider/Model/
// Prompt become comma-joined summaries rather than silently picking the
// last cell's, which would misrepresent a matrix run as single-provider.
func combinedMeta(cells []cell, nPolicy string, ts time.Time) eval.Meta {
	return eval.Meta{
		Provider:  strings.Join(distinctProviders(cells), ","),
		Model:     "(varies by provider — see per-row Model in -out JSON)",
		Prompt:    strings.Join(distinctPrompts(cells), ","),
		NPolicy:   nPolicy,
		Timestamp: ts,
	}
}

func distinctProviders(cells []cell) []string {
	var out []string
	for _, c := range cells {
		out = appendUnique(out, c.Provider)
	}
	return out
}

func distinctPrompts(cells []cell) []string {
	var out []string
	for _, c := range cells {
		out = appendUnique(out, c.PromptName)
	}
	return out
}

func countDistinctProviders(cells []cell) int { return len(distinctProviders(cells)) }
func countDistinctPrompts(cells []cell) int   { return len(distinctPrompts(cells)) }

// appendUnique appends v to ss unless it's already present, preserving
// first-seen order — used to build the distinct provider/variant lists for
// the pre-flight line and combinedMeta without a set type.
func appendUnique(ss []string, v string) []string {
	if slices.Contains(ss, v) {
		return ss
	}
	return append(ss, v)
}

// splitCSV splits a comma-separated flag value, trimming whitespace and
// dropping empties, so "codestral, anthropic," is two entries not three.
func splitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// runOnlyFlagNames are the flags that only mean something for an actual run.
// The terminal modes (-diff, -import, -judge-validate) exit before reaching
// that code, so any of these being set alongside one is surfaced as an error
// rather than silently dropped.
var runOnlyFlagNames = []string{"n", "cases", "out", "print-json", "providers", "prompts", "matrix", "model"}

// setRunOnlyFlags returns which of runOnlyFlagNames were explicitly passed on
// the command line, each rendered as "-name", in flag-declaration order.
func setRunOnlyFlags(setFlags map[string]bool) []string {
	var ignored []string
	for _, name := range runOnlyFlagNames {
		if setFlags[name] {
			ignored = append(ignored, "-"+name)
		}
	}
	return ignored
}

// runDiff loads both scorecard dumps, renders the text diff to stdout, and
// exit(1)s if anything regressed — the CI/pre-commit gate.
func runDiff(beforePath, afterPath string) {
	before, err := loadRunFile(beforePath)
	if err != nil {
		log.Fatalf("eval: -diff: %v", err)
	}
	after, err := loadRunFile(afterPath)
	if err != nil {
		log.Fatalf("eval: -diff: %v", err)
	}

	report := eval.DiffRuns(before, after)
	if err := report.Text(os.Stdout); err != nil {
		log.Fatalf("eval: -diff: writing report: %v", err)
	}

	if report.Regressed() {
		os.Exit(1)
	}
}

// loadRunFile opens path and decodes it as a scorecard dump, wrapping any
// failure with the path so a bad argument never resolves to a silent
// zero-value Run that would misread as "nothing changed".
func loadRunFile(path string) (eval.Run, error) {
	f, err := os.Open(path)
	if err != nil {
		return eval.Run{}, fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close()

	run, err := eval.LoadRun(f)
	if err != nil {
		return eval.Run{}, fmt.Errorf("loading %s: %w", path, err)
	}
	return run, nil
}

// runImport reads a §12 metrics events.jsonl, prints one compilable case
// stub per importable row to stdout, then prints ImportStats to stderr so a
// near-total drop of the log is visible rather than looking like a small,
// healthy import. Never runs any cases.
func runImport(path string) {
	f, err := os.Open(path)
	if err != nil {
		log.Fatalf("eval: -import: opening %s: %v", path, err)
	}
	defer f.Close()

	cases, stats, err := eval.ImportEvents(f, eval.DefaultImportOptions())
	if err != nil {
		log.Fatalf("eval: -import: reading %s: %v", path, err)
	}

	for i, ic := range cases {
		id := fmt.Sprintf("IMPORT-%d", i+1)
		if err := eval.RenderCaseStub(os.Stdout, ic, id); err != nil {
			log.Fatalf("eval: -import: rendering stub %s: %v", id, err)
		}
	}

	fmt.Fprintf(os.Stderr, "\neval: import stats for %s:\n", path)
	fmt.Fprintf(os.Stderr, "  lines read:               %d\n", stats.LinesRead)
	fmt.Fprintf(os.Stderr, "  imported:                 %d\n", stats.Imported)
	fmt.Fprintf(os.Stderr, "  duplicates:               %d\n", stats.Duplicates)
	fmt.Fprintf(os.Stderr, "  skipped, not a request:   %d\n", stats.SkippedNotRequest)
	fmt.Fprintf(os.Stderr, "  skipped, no raw text:     %d\n", stats.SkippedNoRawText)
	fmt.Fprintf(os.Stderr, "  skipped, malformed JSON:  %d\n", stats.SkippedMalformedJSON)
	fmt.Fprintf(os.Stderr, "  skipped, bad buf prefix:  %d\n", stats.SkippedBadPrefix)
	fmt.Fprintf(os.Stderr, "  skipped, trigger filter:  %d\n", stats.SkippedTriggerFilter)
	fmt.Fprintf(os.Stderr, "  skipped, likely secret:   %d\n", stats.SkippedLikelySecret)
	fmt.Fprintf(os.Stderr, "  skipped, over -MaxCases:  %d\n", stats.SkippedOverCap)

	if stats.LinesRead > 0 && stats.Imported*10 < stats.LinesRead {
		fmt.Fprintf(os.Stderr, "eval: WARNING - imported only %d of %d lines read (<10%%); "+
			"check the skip counts above before assuming this is a healthy import\n", stats.Imported, stats.LinesRead)
	}
}

// judgeAgreementGate is the plan doc's non-negotiable bar ("The judge" ->
// "Validate before trusting"): no judged number (C3/E3/E7/F2) may be quoted
// until some candidate judge clears this on the hand-labeled set.
const judgeAgreementGate = 0.90

// runJudgeValidate resolves candidate judge models from -judges (default:
// the configured judge), scores each against -labels, prints a ranked table
// plus disagreements, and exit(1)s unless the best clears
// judgeAgreementGate — a gate, not just a report.
func runJudgeValidate(judgesFlag, labelsPath string) {
	cfg := eval.JudgeConfigFromEnv()
	if cfg.APIKey == "" {
		log.Fatalf("eval: -judge-validate requires a judge API key; set ZSH_AUTOPILOT_EVAL_JUDGE_KEY " +
			"(see .docs/eval_harness_plan.md, \"The judge\")")
	}

	models := splitCSV(judgesFlag)
	if len(models) == 0 {
		models = []string{cfg.Model}
	}

	judges := make([]eval.Judge, 0, len(models))
	for _, m := range models {
		jcfg := cfg
		jcfg.Model = m
		j, err := eval.NewGeminiJudge(jcfg)
		if err != nil {
			log.Fatalf("eval: -judge-validate: building judge %q: %v", m, err)
		}
		judges = append(judges, j)
	}

	f, err := os.Open(labelsPath)
	if err != nil {
		log.Fatalf("eval: -judge-validate: no hand labels at %s (write them first — see .docs/eval_harness_plan.md, "+
			"\"The judge\": \"hand-label 30 outputs\"): %v", labelsPath, err)
	}
	defer f.Close()

	cases := eval.Cases()
	labels, err := eval.LoadLabels(f, cases)
	if err != nil {
		log.Fatalf("eval: -judge-validate: %v", err)
	}
	if len(labels) == 0 {
		log.Fatalf("eval: -judge-validate: %s contains no labels", labelsPath)
	}

	if fracPass, imbalanced := eval.LabelImbalance(labels); imbalanced {
		fmt.Fprintf(os.Stderr, "eval: WARNING - label set is imbalanced (%.0f%% pass, n=%d); raw agreement is "+
			"inflated by chance here, read Kappa, not just Agreement\n", fracPass*100, len(labels))
	}

	fmt.Fprintf(os.Stderr, "eval: scoring %d judge(s) against %d hand label(s) from %s (verdict cache bypassed)\n",
		len(judges), len(labels), labelsPath)

	scores := eval.ValidateJudges(context.Background(), labels, cases, judges)
	ranked := eval.RankByAgreementPerDollar(scores)

	printJudgeScoreboard(os.Stdout, ranked)
	printJudgeDisagreements(os.Stdout, ranked)

	bestAgreement := 0.0
	for _, s := range ranked {
		if s.Agreement > bestAgreement {
			bestAgreement = s.Agreement
		}
	}
	if bestAgreement < judgeAgreementGate {
		fmt.Fprintf(os.Stderr, "eval: FAIL - best judge agreement %.1f%% is below the %.0f%% gate; "+
			"per the plan doc, the rubric is the problem — rewrite, re-label, re-measure before quoting any judged number\n",
			bestAgreement*100, judgeAgreementGate*100)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "eval: PASS - best judge agreement %.1f%% clears the %.0f%% gate\n",
		bestAgreement*100, judgeAgreementGate*100)
}

// printJudgeScoreboard prints the ranked judge/agreement/kappa/errors/cost
// table -judge-validate's spec calls for.
func printJudgeScoreboard(w io.Writer, ranked []eval.JudgeScore) {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "judge\tagreement\tkappa\terrors\tcost_usd\tagreement/$")
	for _, s := range ranked {
		fmt.Fprintf(tw, "%s\t%.1f%% (%d/%d)\t%.3f\t%d\t$%.4f\t%s\n",
			s.Judge, s.Agreement*100, s.Agreed, s.Total, s.Kappa, s.Errors, s.CostUSD, formatAgreementPerDollar(s))
	}
	tw.Flush()
	// The "errors" column is a bare count; print the first error per judge
	// that had one so a total-failure row isn't just "0.0% (0/0) errors 2".
	for _, s := range ranked {
		if s.Errors > 0 && s.FirstError != "" {
			fmt.Fprintf(w, "  %s: first error — %s\n", s.Judge, s.FirstError)
		}
	}
}

func formatAgreementPerDollar(s eval.JudgeScore) string {
	if s.CostUSD <= 0 {
		if s.Agreement > 0 {
			return "inf (cost unpriced)"
		}
		return "0"
	}
	return fmt.Sprintf("%.1f", s.Agreement/s.CostUSD)
}

// printJudgeDisagreements prints every sample where a candidate's verdict
// differed from the human label, with the judge's stated reason — needed to
// tell whether the JUDGE is wrong or the RUBRIC is ambiguous.
func printJudgeDisagreements(w io.Writer, ranked []eval.JudgeScore) {
	for _, s := range ranked {
		if len(s.Disagreements) == 0 {
			continue
		}
		fmt.Fprintf(w, "\n%s: %d disagreement(s)\n", s.Judge, len(s.Disagreements))
		for _, d := range s.Disagreements {
			fmt.Fprintf(w, "  [%s] suggestion=%q human=%s judge=%s reason=%q\n",
				d.CaseID, d.Suggestion, d.HumanVerdict, d.JudgeVerdict, d.JudgeReason)
		}
	}
}
