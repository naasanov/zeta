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
	"log"
	"os"
	"strings"
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
		outPath       = flag.String("out", "", "write the JSON report to this path (in addition to the text scorecard on stdout)")
		dryRun        = flag.Bool("dry-run", false, "use a scripted stub provider instead of a live one (no network); -providers/-matrix are ignored, -variants still selects the prompt-building path")
		providersFlag = flag.String("providers", "codestral", "comma-separated provider brands to run against (\"codestral,anthropic,groq\"); the cheap default is a single provider")
		variantsFlag  = flag.String("variants", "default", "comma-separated prompt variants to run (\"default,fim-commented-history\")")
		matrix        = flag.Bool("matrix", false, "shorthand for every provider (matrixProviders) x every registered variant; the occasional full run, not the default")
		modelOverride = flag.String("model", "", "override the resolved model for every selected provider (see eval.NewLiveProvider); empty keeps each provider's preset default")
		importPath    = flag.String("import", "", "read a §12 metrics events.jsonl, print case stubs + import stats to stdout/stderr, and exit without running anything")
		diffMode      = flag.Bool("diff", false, "compare two scorecard JSON dumps (positional args: old.json new.json), print the diff, and exit non-zero if anything regressed; a terminal mode like -import — never runs cases")
	)
	flag.Parse()

	// setFlags is which flags were actually passed on the command line (as
	// opposed to left at their default), so the terminal modes below (-diff,
	// -import) can detect run-only flags that would otherwise be silently
	// ignored rather than quietly dropping them — see runOnlyFlagNames.
	setFlags := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { setFlags[f.Name] = true })

	if *diffMode && *importPath != "" {
		log.Fatalf("eval: -diff and -import are mutually exclusive terminal modes; run one at a time")
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

	start := time.Now()

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

		runner := &eval.Runner{Provider: p, Variant: c.Variant, Limiter: limiter, FixedN: *n}
		results := runner.Run(context.Background(), cases)
		allResults = append(allResults, results...)

		lastMeta = eval.Meta{
			Provider:  p.Name(),
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

	if err := eval.Text(os.Stdout, allResults, meta); err != nil {
		log.Fatalf("eval: writing text report: %v", err)
	}
	fmt.Fprintf(os.Stderr, "eval: done in %s\n", elapsed.Round(time.Millisecond))

	if *outPath != "" {
		f, err := os.Create(*outPath)
		if err != nil {
			log.Fatalf("eval: creating -out file: %v", err)
		}
		defer f.Close()
		if err := eval.JSON(f, allResults, meta); err != nil {
			log.Fatalf("eval: writing JSON report: %v", err)
		}
	}
}

// cell is one (provider brand, prompt variant) combination — one row of the
// matrix this command assembles from -providers/-variants/-matrix.
type cell struct {
	Provider string // brand name; "stub" under -dry-run
	Variant  eval.Variant
}

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

	p, err := eval.NewLiveProvider(c.Provider, modelOverride, config.DefaultMaxTokens)
	if err != nil {
		return nil, nil, err
	}
	return p, eval.LimiterForBrand(c.Provider), nil
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
	for _, s := range ss {
		if s == v {
			return ss
		}
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
var runOnlyFlagNames = []string{"n", "cases", "out", "providers", "variants", "matrix", "model"}

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
