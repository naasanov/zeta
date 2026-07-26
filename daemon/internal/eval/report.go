package eval

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"
	"time"
)

// Meta describes one Runner invocation for a report's header/footer and for
// the Part 4 JSON diff to key off of.
type Meta struct {
	Provider  string
	Model     string
	Variant   string
	NPolicy   string // e.g. "adaptive(min=3,max=10)" or "fixed=5"
	Timestamp time.Time
}

// dump is the JSON wire shape for a full report: Meta plus every CaseResult.
// It's what JSON writes and what a Part 4 -diff would read back.
type dump struct {
	Meta    Meta         `json:"meta"`
	Results []CaseResult `json:"results"`
}

// JSON writes a machine-readable dump of results and meta to w, for the
// Part 4 scorecard diff (`go run ./cmd/eval -diff old.json new.json`).
func JSON(w io.Writer, results []CaseResult, meta Meta) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(dump{Meta: meta, Results: results})
}

// rate formats a k/N fraction as a percentage; "n/a" when n == 0 so a
// zero-graded assertion doesn't print a misleading "0%".
func rate(k, n int) string {
	if n == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.0f%%", 100*float64(k)/float64(n))
}

func passLabel(pass bool) string {
	if pass {
		return "PASS"
	}
	return "FAIL"
}

// Text writes an aligned scorecard to w: a trip-wire section (loudest,
// printed first, only when something tripped), then one row per
// (case, assertion) grouped by category, then a footer summarizing sampling
// policy and totals.
//
// Category grouping preserves the order categories first appear in results
// (== the order Cases were passed to Runner.Run, since Run writes results by
// index), so the report's order is deterministic and matches the input.
func Text(w io.Writer, results []CaseResult, meta Meta) error {
	if err := writeHeader(w, meta); err != nil {
		return err
	}
	if err := writeTripWires(w, results); err != nil {
		return err
	}
	if err := writeScorecard(w, results); err != nil {
		return err
	}
	return writeFooter(w, results, meta)
}

func writeHeader(w io.Writer, meta Meta) error {
	_, err := fmt.Fprintf(w, "eval report: provider=%s model=%s variant=%s n-policy=%s time=%s\n\n",
		orNA(meta.Provider), orNA(meta.Model), orNA(meta.Variant), orNA(meta.NPolicy),
		meta.Timestamp.Format(time.RFC3339))
	return err
}

func orNA(s string) string {
	if s == "" {
		return "n/a"
	}
	return s
}

func writeTripWires(w io.Writer, results []CaseResult) error {
	type hit struct {
		caseID, label, offending string
	}
	var hits []hit
	for _, cr := range results {
		for _, ar := range cr.Asserts {
			if ar.Polarity == TripWire && !ar.Pass {
				hits = append(hits, hit{cr.CaseID, ar.Label, ar.FirstOffending})
			}
		}
	}

	if len(hits) == 0 {
		_, err := fmt.Fprintf(w, "TRIP WIRES: none tripped.\n\n")
		return err
	}

	if _, err := fmt.Fprintf(w, "TRIP WIRES TRIPPED (%d) — these are defects, not quality judgments:\n", len(hits)); err != nil {
		return err
	}
	for _, h := range hits {
		if _, err := fmt.Fprintf(w, "  [%s] %s: offending output = %q\n", h.caseID, h.label, h.offending); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(w)
	return err
}

func writeScorecard(w io.Writer, results []CaseResult) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "CATEGORY\tCASE\tASSERTION\tPOL\tk/N\tRATE\tTHRESH\tRESULT"); err != nil {
		return err
	}

	seen := map[string]bool{}
	var order []string
	for _, cr := range results {
		if !seen[cr.Category] {
			seen[cr.Category] = true
			order = append(order, cr.Category)
		}
	}

	for _, cat := range order {
		for _, cr := range results {
			if cr.Category != cat {
				continue
			}
			for _, ar := range cr.Asserts {
				thresh := "-"
				if ar.Polarity == Must || ar.Polarity == MustNot {
					thresh = fmt.Sprintf("%.0f%%", 100*ar.Threshold)
				}
				if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d/%d\t%s\t%s\t%s\n",
					cat, cr.CaseID, ar.Label, ar.Polarity, ar.Present, ar.Graded,
					rate(ar.Present, ar.Graded), thresh, passLabel(ar.Pass)); err != nil {
					return err
				}
			}
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	_, err := fmt.Fprintln(w)
	return err
}

func writeFooter(w io.Writer, results []CaseResult, meta Meta) error {
	var saturated, escalated []string
	totalRuns, totalErrors, totalGraderErrors := 0, 0, 0
	var unevaluated []string
	for _, cr := range results {
		totalRuns += cr.Runs
		totalErrors += cr.Errors
		// Escalation is reported by the Runner, not inferred from
		// Runs+Errors: that inference breaks the moment MinRuns is
		// customised or a case errors out before reaching the threshold.
		if cr.Escalated {
			escalated = append(escalated, cr.CaseID)
		} else {
			saturated = append(saturated, cr.CaseID)
		}
		for _, ar := range cr.Asserts {
			totalGraderErrors += ar.GraderErrors
			// Graded == 0 means the assertion never actually ran. It is
			// scored as a failure, but it needs calling out separately —
			// "failed" and "never evaluated" demand different fixes.
			if ar.Graded == 0 {
				unevaluated = append(unevaluated, cr.CaseID+"/"+ar.Label)
			}
		}
	}

	if _, err := fmt.Fprintf(w, "saturated at min runs (%d): %s\n", defaultMinRuns, joinOrNone(saturated)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "escalated past min runs: %s\n", joinOrNone(escalated)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "total runs: %d, total errors: %d, grader errors: %d\n", totalRuns, totalErrors, totalGraderErrors); err != nil {
		return err
	}
	if len(unevaluated) > 0 {
		if _, err := fmt.Fprintf(w, "NEVER EVALUATED (0 graded samples, scored as failures): %s\n", joinOrNone(unevaluated)); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(w, "caveat: per-case rates at N<=10 are triage, not results; "+
		"do not read a 7/10 vs 8/10 difference as a difference.")
	return err
}

func joinOrNone(ids []string) string {
	if len(ids) == 0 {
		return "(none)"
	}
	s := ids[0]
	for _, id := range ids[1:] {
		s += ", " + id
	}
	return s
}
