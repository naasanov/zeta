// Command eval is the live-provider suggestion-quality evaluation harness
// (see .docs/eval_harness_plan.md). It is a `main` package, not a test,
// precisely so it never runs under `go test ./...`: evals are slow, cost
// money, hit live rate limits, and are non-deterministic by construction.
// `go build ./...` still compile-checks it.
//
// This is Part 1's wiring: flags and a placeholder case list only. Part 2
// supplies the real case corpus (internal/eval/cases*.go); Part 3 adds a
// judge; Part 4 adds provider/variant axes and -diff. Until then, -dry-run
// (a scripted eval.StubProvider, no network) is the only way to run this
// end-to-end.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/naasanov/zsh-autopilot/daemon/internal/eval"
	"github.com/naasanov/zsh-autopilot/daemon/internal/provider"
)

// maxN mirrors the plan doc's guardrail ("-n is hard-capped at 10"):
// adaptive sampling already escalates to at most this many runs, so a
// fixed-N override above it would just waste calls for no more signal.
const maxN = 10

func main() {
	var (
		n       = flag.Int("n", 0, "fixed run count per case (0 = adaptive, hard-capped at 10)")
		caseSel = flag.String("cases", "", "comma-separated case selectors: IDs (\"A1,B2\"), categories (\"syntax\"), or ID globs (\"A*\"); empty runs everything")
		outPath = flag.String("out", "", "write the JSON report to this path (in addition to the text scorecard on stdout)")
		dryRun  = flag.Bool("dry-run", false, "use a scripted stub provider instead of a live one (no network)")
	)
	flag.Parse()

	if *n > maxN {
		log.Fatalf("eval: -n=%d exceeds the hard cap of %d; the plan doc treats anything higher as wasted "+
			"budget over what adaptive sampling already reaches (see .docs/eval_harness_plan.md, "+
			"\"No temperature knob\"/guardrail note)", *n, maxN)
	}
	if *n < 0 {
		log.Fatalf("eval: -n=%d is invalid; use 0 for adaptive sampling", *n)
	}

	var p provider.Provider
	if *dryRun {
		p = eval.NewStubProvider(
			eval.StubResult{Output: " status"},
			eval.StubResult{Output: " status"},
			eval.StubResult{Output: " status"},
		)
	} else {
		log.Fatal("eval: only -dry-run is wired up in Part 1; a live provider needs Part 4's provider/config wiring")
	}

	cases, err := eval.Select(eval.Cases(), *caseSel)
	if err != nil {
		// Fatal rather than "run what matched": a selector typo that quietly
		// narrows the run exits 0 with a clean scorecard for cases that never
		// executed. See eval.Select's doc comment.
		log.Fatalf("eval: -cases=%q: %v", *caseSel, err)
	}

	runner := &eval.Runner{Provider: p, FixedN: *n}
	results := runner.Run(context.Background(), cases)

	nPolicy := "adaptive(min=3,max=10)"
	if *n > 0 {
		nPolicy = fmt.Sprintf("fixed=%d", *n)
	}
	meta := eval.Meta{
		Provider:  p.Name(),
		Model:     p.Model(),
		Variant:   eval.DefaultVariant().Name,
		NPolicy:   nPolicy,
		Timestamp: time.Now(),
	}

	if err := eval.Text(os.Stdout, results, meta); err != nil {
		log.Fatalf("eval: writing text report: %v", err)
	}

	if *outPath != "" {
		f, err := os.Create(*outPath)
		if err != nil {
			log.Fatalf("eval: creating -out file: %v", err)
		}
		defer f.Close()
		if err := eval.JSON(f, results, meta); err != nil {
			log.Fatalf("eval: writing JSON report: %v", err)
		}
	}
}
