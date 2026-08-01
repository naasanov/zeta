// Command eval is the live-provider suggestion-quality evaluation harness
// (see .docs/eval_harness_plan.md). It is a `main` package, not a test,
// precisely so it never runs under `go test ./...`: evals are slow, cost
// money, hit live rate limits, and are non-deterministic by construction.
// `go build ./...` still compile-checks it.
//
// Parts 1-3 built the harness skeleton, case corpus, and judge, all driven
// by a scripted stub provider (-dry-run). This is Part 4's wiring: real
// providers (internal/eval/live.go), the variant axis
// (internal/eval/variants.go), and the -providers/-variants/-matrix/-model
// flags that assemble a (provider, variant) matrix out of them, plus -import
// (Part 3b's importer, wired here for the first time).
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
	"github.com/naasanov/zsh-autopilot/daemon/internal/provider"
)

// maxN mirrors the plan doc's guardrail ("-n is hard-capped at 10"):
// adaptive sampling already escalates to at most this many runs, so a
// fixed-N override above it would just waste calls for no more signal. It
// also stands in for the adaptive ceiling (eval.defaultMaxRuns, unexported)
// when estimating a pre-flight worst-case call count below.
const maxN = 10

// matrixProviders is what `-matrix` expands -providers to: the plan doc's
// "3 providers" full matrix. ollama and the openai escape hatch are
// deliberately excluded — ollama's model is an untested local placeholder
// (config.go's presets comment) and openai has no sensible default
// base_url/model to run unattended, so neither belongs in an unattended
// full-matrix run.
var matrixProviders = []string{"codestral", "anthropic", "groq"}

func main() {
	var (
		n             = flag.Int("n", 0, "fixed run count per case (0 = adaptive, hard-capped at 10)")
		caseSel       = flag.String("cases", "", "comma-separated case selectors: IDs (\"A1,B2\"), categories (\"syntax\"), or ID globs (\"A*\"); empty runs everything")
		outPath       = flag.String("out", "", "write the JSON report to this path (in addition to the text scorecard on stdout); if it's an existing directory, a timestamped provider/variant-named file is created inside it")
		printJSON     = flag.Bool("print-json", false, "print the JSON report to stdout before the scorecard, with embedded newlines (e.g. in the rendered Prompt field) shown literally rather than escaped — NOT valid JSON, a terminal-reading convenience only")
		dryRun        = flag.Bool("dry-run", false, "use a scripted stub provider instead of a live one (no network); -providers/-matrix are ignored, -variants still selects the prompt-building path")
		providersFlag = flag.String("providers", "codestral", "comma-separated provider brands to run against (\"codestral,anthropic,groq\"); the cheap default is a single provider")
		variantsFlag  = flag.String("variants", "default", "comma-separated prompt variants to run (\"default,fim-commented-history\")")
		matrix        = flag.Bool("matrix", false, "shorthand for every provider (matrixProviders) x every registered variant; the occasional full run, not the default")
		modelOverride = flag.String("model", "", "override the resolved model for every selected provider (see eval.NewLiveProvider); empty keeps each provider's preset default")
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

	cells := resolveCells(*dryRun, *matrix, *providersFlag, *variantsFlag)

	perCaseMax := maxN
	nPolicy := "adaptive(min=3,max=10)"
	if *n > 0 {
		perCaseMax = *n
		nPolicy = fmt.Sprintf("fixed=%d", *n)
	}
	maxCalls := len(cells) * len(cases) * perCaseMax
	fmt.Fprintf(os.Stderr, "eval: %d cell(s) (%d provider(s) x %d variant(s)) x %d case(s), up to %d run(s)/case => up to %d total provider calls\n",
		len(cells), countDistinctProviders(cells), countDistinctVariants(cells), len(cases), perCaseMax, maxCalls)

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
			// broken one. See eval.NewLiveProvider's doc comment on why a
			// missing key must never fall back to a stub.
			log.Fatalf("eval: provider %q: %v", c.Provider, err)
		}

		// One row per cell, symbols streaming out as cases finish. Progress
		// goes to stderr so a piped or -out'd stdout stays a clean scorecard.
		fmt.Fprintf(os.Stderr, "%-*s  ", labelWidth, cellLabel(c))
		var passed, failed, tripped, errored int
		runner := &eval.Runner{
			Provider: p, Variant: c.Variant, Limiter: limiter, FixedN: *n,
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
			Variant:   c.Variant.Name,
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

// defaultOutFilename builds the file name -out writes to when it's pointed
// at a directory instead of a file: a "20060102-150405" timestamp prefix
// (alpha-sorting a directory of these also sorts them chronologically, oldest
// first) followed by the provider x variant combo that produced the report,
// so `ls` alone tells the two things you'd otherwise open the file to check.
func defaultOutFilename(meta eval.Meta) string {
	return fmt.Sprintf("%s_%s_%s.json",
		meta.Timestamp.Format("20060102-150405"),
		sanitizeForFilename(meta.Provider),
		sanitizeForFilename(meta.Variant))
}

// sanitizeForFilename turns a comma-joined meta field (e.g.
// "codestral,anthropic") into a filename-safe segment ("codestral+anthropic")
// — combinedMeta already comma-joins multi-cell runs, so this only needs to
// swap the one separator that can't appear in a path component.
func sanitizeForFilename(s string) string {
	return strings.ReplaceAll(s, ",", "+")
}

// cell is one (provider brand, prompt variant) combination — one row of the
// matrix this command assembles from -providers/-variants/-matrix.
type cell struct {
	Provider string // brand name; "stub" under -dry-run
	Variant  eval.Variant
}

// cellLabel is the row prefix for a cell's live progress line, e.g.
// "codestral/default".
func cellLabel(c cell) string { return c.Provider + "/" + c.Variant.Name }

// resolveCells assembles the matrix. Under -dry-run, -providers/-matrix are
// ignored entirely — Part 1's -dry-run behavior stays exactly a single
// scripted stub, no network, no provider axis — but -variants still selects
// which prompt-building path the stub run exercises, since that's a pure
// in-process concern with no cost or network attached.
func resolveCells(dryRun, matrix bool, providersFlag, variantsFlag string) []cell {
	variantNames := splitCSV(variantsFlag)
	if matrix {
		for _, v := range eval.AllVariants() {
			variantNames = appendUnique(variantNames, v.Name)
		}
	}
	variants := make([]eval.Variant, 0, len(variantNames))
	for _, vn := range variantNames {
		v, err := eval.VariantByName(vn)
		if err != nil {
			log.Fatalf("eval: -variants=%q: %v", variantsFlag, err)
		}
		variants = append(variants, v)
	}

	if dryRun {
		cells := make([]cell, 0, len(variants))
		for _, v := range variants {
			cells = append(cells, cell{Provider: "stub", Variant: v})
		}
		return cells
	}

	providerNames := splitCSV(providersFlag)
	if matrix {
		providerNames = matrixProviders
	}

	cells := make([]cell, 0, len(providerNames)*len(variants))
	for _, pn := range providerNames {
		for _, v := range variants {
			cells = append(cells, cell{Provider: pn, Variant: v})
		}
	}
	return cells
}

// buildCellProvider constructs the provider.Provider and eval.Limiter for
// one matrix cell. Under -dry-run it always returns a freshly scripted
// StubProvider (matching Part 1's -dry-run script exactly) with a
// NoopLimiter, regardless of c.Provider, since -dry-run cells are always
// {Provider: "stub", ...}.
func buildCellProvider(c cell, dryRun bool, modelOverride string) (provider.Provider, eval.Limiter, error) {
	if dryRun {
		return eval.NewStubProvider(
			eval.StubResult{Output: " status"},
			eval.StubResult{Output: " status"},
			eval.StubResult{Output: " status"},
		), eval.NoopLimiter{}, nil
	}

	// The variant supplies the FIM renderer (nil for prompt-content-only
	// variants), so a prompt-SHAPE variant reaches the codestral adapter —
	// Runner only ever applies Variant.Build, which runs before rendering.
	p, err := eval.NewLiveProvider(c.Provider, modelOverride, config.DefaultMaxTokens, c.Variant.FIMRenderer)
	if err != nil {
		return nil, nil, err
	}
	return p, eval.LimiterForBrand(c.Provider), nil
}

// concurrencyFor returns the Runner.Concurrency to use for a cell's limiter:
// 1 for a real *eval.RateLimiter (see Runner.Concurrency's doc comment on why
// spreading a shared per-minute budget across several concurrent workers only
// adds queuing latency, never throughput), 0 (Runner's own default) for
// anything else, i.e. eval.NoopLimiter.
func concurrencyFor(limiter eval.Limiter) int {
	if _, ok := limiter.(*eval.RateLimiter); ok {
		return 1
	}
	return 0
}

// combinedMeta builds the report Meta for a multi-cell run: Provider/Model/
// Variant become comma-joined summaries (there is no single value to show)
// rather than silently picking the last cell's, which would misrepresent a
// matrix run as a single-provider run in the header.
func combinedMeta(cells []cell, nPolicy string, ts time.Time) eval.Meta {
	return eval.Meta{
		Provider:  strings.Join(distinctProviders(cells), ","),
		Model:     "(varies by provider — see per-row Model in -out JSON)",
		Variant:   strings.Join(distinctVariants(cells), ","),
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

func distinctVariants(cells []cell) []string {
	var out []string
	for _, c := range cells {
		out = appendUnique(out, c.Variant.Name)
	}
	return out
}

func countDistinctProviders(cells []cell) int { return len(distinctProviders(cells)) }
func countDistinctVariants(cells []cell) int  { return len(distinctVariants(cells)) }

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

// runOnlyFlagNames are the flags that only mean something for an actual run
// (cases + a provider matrix + a report). Both terminal modes (-diff,
// -import) exit before ever reaching that code, so any of these being set
// alongside one would be silently dropped rather than honored — surfacing
// that explicitly is better than a report that quietly ran a smaller (or
// different) thing than what was asked for.
var runOnlyFlagNames = []string{"n", "cases", "out", "print-json", "providers", "variants", "matrix", "model"}

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

// runDiff is -diff's whole flow: load both scorecard dumps, render the text
// diff to stdout, and exit(1) if anything regressed — the CI/pre-commit gate
// the plan doc's "Regression use" section describes. It never touches
// providers, cases, or the report writer used by a real run.
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
// failure (missing file, unreadable, not valid JSON, wrong shape) with the
// path so a bad "before"/"after" argument never resolves to a silent
// zero-value Run that would misread as "nothing changed" — see LoadRun's own
// doc comment on exactly that failure mode.
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

// runImport is -import's whole flow: read a §12 metrics events.jsonl, print
// one compilable case stub per importable row to stdout (Part 3b's
// RenderCaseStub), then print ImportStats to stderr so a near-total drop of
// the log (nearly everything skipped as malformed/no-raw-text/secret-like)
// is visible rather than indistinguishable from a small, healthy import. It
// never runs any cases — this is purely the harvesting step described in
// the plan doc's Part 3b.
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

// runJudgeValidate is -judge-validate's whole flow (Part 5's build half):
// resolve one or more candidate judge models from -judges (defaulting to the
// single configured judge), score each against -labels via
// eval.ValidateJudges, print a ranked table plus the disagreement detail,
// and exit(1) unless the best-agreeing candidate clears judgeAgreementGate —
// so this reads as a gate, not just a report. It never constructs a
// suggestion provider and never runs a case.
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
	// The table's "errors" column is a bare count — a judge that fails every
	// call renders as "0.0% (0/0)  errors 2" with nothing pointing at why.
	// Print the first error per judge that had one, right below the table,
	// same reasoning as report.go's NEVER EVALUATED line.
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
// differed from the human label, with the judge's own stated reason — the
// point of falling below the gate is finding out whether the JUDGE is wrong
// or the RUBRIC is ambiguous, and that requires reading these.
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
