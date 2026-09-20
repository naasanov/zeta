package eval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"time"
)

// ANSI codes for the report, gated on NO_COLOR (https://no-color.org): any
// non-empty value disables color.
const (
	ansiReset  = "\x1b[0m"
	ansiBold   = "\x1b[1m"
	ansiRed    = "\x1b[31m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
)

func noColor() bool {
	return os.Getenv("NO_COLOR") != ""
}

func colorize(code, s string) string {
	if code == "" || noColor() {
		return s
	}
	return code + s + ansiReset
}

func ColorizeSymbol(sym rune) string {
	switch sym {
	case '.':
		return colorize(ansiGreen, string(sym))
	case 'x':
		return colorize(ansiRed, string(sym))
	case '!':
		return colorize(ansiRed+ansiBold, string(sym))
	default: // 'E'
		return colorize(ansiYellow, string(sym))
	}
}

func ColorGood(s string) string { return colorize(ansiGreen, s) }
func ColorBad(s string) string  { return colorize(ansiRed, s) }
func ColorWarn(s string) string { return colorize(ansiYellow, s) }

type Meta struct {
	Provider  string    `json:"provider"`
	Model     string    `json:"model"`
	Prompt    string    `json:"prompt"`
	NPolicy   string    `json:"n_policy"` // e.g. "adaptive(min=3,max=10)" or "fixed=5"
	Timestamp time.Time `json:"timestamp"`
}

type dump struct {
	Meta    Meta         `json:"meta"`
	Results []CaseResult `json:"results"`
}

func JSON(w io.Writer, results []CaseResult, meta Meta) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(dump{Meta: meta, Results: results})
}

// PrettyJSON's output is NOT valid JSON.
func PrettyJSON(w io.Writer, results []CaseResult, meta Meta) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(dump{Meta: meta, Results: results}); err != nil {
		return err
	}
	_, err := fmt.Fprint(w, strings.ReplaceAll(buf.String(), `\n`, "\n"))
	return err
}

func rate(k, n int) string {
	if n == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.0f%%", 100*float64(k)/float64(n))
}

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
	_, err := fmt.Fprintf(w, "eval report: provider=%s model=%s prompt=%s n-policy=%s time=%s\n\n",
		orNA(meta.Provider), orNA(meta.Model), orNA(meta.Prompt), orNA(meta.NPolicy),
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
		cell, caseID, label, offending string
	}
	var hits []hit
	for _, cr := range results {
		for _, ar := range cr.Asserts {
			if ar.Polarity == TripWire && !ar.Pass {
				hits = append(hits, hit{CellLabel(cr), cr.CaseID, ar.Label, ar.FirstOffending})
			}
		}
	}

	if len(hits) == 0 {
		_, err := fmt.Fprintf(w, "%s\n\n", colorize(ansiGreen, "TRIP WIRES: none tripped."))
		return err
	}

	header := fmt.Sprintf("TRIP WIRES TRIPPED (%d) — these are defects, not quality judgments:", len(hits))
	if _, err := fmt.Fprintf(w, "%s\n", colorize(ansiRed+ansiBold, header)); err != nil {
		return err
	}
	for _, h := range hits {
		if _, err := fmt.Fprintf(w, "  [%s] %s %s: offending output = %q\n", h.cell, h.caseID, h.label, h.offending); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(w)
	return err
}

// CellLabel is a CaseResult's (provider, prompt) cell: the scorecard's column key.
func CellLabel(cr CaseResult) string {
	provider := orNA(cr.Provider)
	if cr.PromptName == "" {
		return provider
	}
	return provider + "/" + cr.PromptName
}

type assertionKey struct {
	Category string
	CaseID   string
	Label    string
}

// writeScorecard renders the pivot: one row per (case, assertion), one
// column per cell.
func writeScorecard(w io.Writer, results []CaseResult) error {
	cells := orderedCells(results)
	rows, byCellRow := scorecardRows(results)

	// tabwriter sizes columns from raw byte length, so cellVerdict emits
	// placeholder bytes; decorateGlyphs swaps in real glyphs after Flush.
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)

	header := "CATEGORY\tCASE\tASSERTION\tPOL\tTHRESH"
	for _, c := range cells {
		header += "\t" + c
	}
	if _, err := fmt.Fprintln(tw, header); err != nil {
		return err
	}

	for _, key := range rows {
		var pol Polarity
		thresh := "-"
		for _, c := range cells {
			if ar, ok := byCellRow[c][key]; ok {
				pol = ar.Polarity
				if ar.Polarity == Must || ar.Polarity == MustNot {
					thresh = fmt.Sprintf("%.0f%%", 100*ar.Threshold)
				}
				break
			}
		}

		line := fmt.Sprintf("%s\t%s\t%s\t%s\t%s", key.Category, key.CaseID, key.Label, pol, thresh)
		for _, c := range cells {
			ar, ok := byCellRow[c][key]
			if !ok {
				// Distinct from a failure: this cell never produced this (case, assertion).
				line += "\t—"
				continue
			}
			line += "\t" + cellVerdict(ar)
		}
		if _, err := fmt.Fprintln(tw, line); err != nil {
			return err
		}
	}

	if err := tw.Flush(); err != nil {
		return err
	}
	if _, err := w.Write(decorateGlyphs(buf.Bytes())); err != nil {
		return err
	}
	_, err := fmt.Fprintln(w)
	return err
}

// Control bytes, so they can't collide with real case/category text.
const (
	glyphPassPH byte = '\x01'
	glyphFailPH byte = '\x02'
	glyphTripPH byte = '\x03'
	glyphErrPH  byte = '\x04'
)

func decorateGlyphs(data []byte) []byte {
	repl := map[byte]string{
		glyphPassPH: colorize(ansiGreen, "."),
		glyphFailPH: colorize(ansiRed, "x"),
		glyphTripPH: colorize(ansiRed+ansiBold, "!"),
		glyphErrPH:  colorize(ansiYellow, "E"),
	}
	out := make([]byte, 0, len(data))
	for _, b := range data {
		if s, ok := repl[b]; ok {
			out = append(out, s...)
			continue
		}
		out = append(out, b)
	}
	return out
}

func cellVerdict(ar AssertionResult) string {
	if ar.Graded == 0 {
		return string(glyphErrPH) + " n/a"
	}
	ph := glyphPassPH
	if !ar.Pass {
		ph = glyphFailPH
		if ar.Polarity == TripWire {
			ph = glyphTripPH
		}
	}
	return string(ph) + " " + rate(ar.Present, ar.Graded)
}

func orderedCells(results []CaseResult) []string {
	seen := map[string]bool{}
	var out []string
	for _, cr := range results {
		label := CellLabel(cr)
		if !seen[label] {
			seen[label] = true
			out = append(out, label)
		}
	}
	return out
}

// scorecardRows returns row keys in first-seen category order, plus a
// cell->row->result lookup.
func scorecardRows(results []CaseResult) ([]assertionKey, map[string]map[assertionKey]AssertionResult) {
	byCell := map[string]map[assertionKey]AssertionResult{}

	var catOrder []string
	seenCat := map[string]bool{}
	var keysByCat = map[string][]assertionKey{}
	seenKey := map[assertionKey]bool{}

	for _, cr := range results {
		label := CellLabel(cr)
		if byCell[label] == nil {
			byCell[label] = map[assertionKey]AssertionResult{}
		}
		if !seenCat[cr.Category] {
			seenCat[cr.Category] = true
			catOrder = append(catOrder, cr.Category)
		}
		for _, ar := range cr.Asserts {
			key := assertionKey{Category: cr.Category, CaseID: cr.CaseID, Label: ar.Label}
			byCell[label][key] = ar
			if !seenKey[key] {
				seenKey[key] = true
				keysByCat[cr.Category] = append(keysByCat[cr.Category], key)
			}
		}
	}

	var rows []assertionKey
	for _, cat := range catOrder {
		rows = append(rows, keysByCat[cat]...)
	}
	return rows, byCell
}

func writeFooter(w io.Writer, results []CaseResult, meta Meta) error {
	cells := orderedCells(results)
	type cellStats struct {
		runs, errors, successes int
		escalated               []string
		latencies               []time.Duration
	}
	stats := map[string]*cellStats{}
	for _, c := range cells {
		stats[c] = &cellStats{}
	}

	totalRuns, totalErrors, totalGraderErrors := 0, 0, 0
	var unevaluated []string
	for _, cr := range results {
		totalRuns += cr.Runs
		totalErrors += cr.Errors
		st := stats[CellLabel(cr)]
		st.runs += cr.Runs
		st.errors += cr.Errors
		// Pooled across every case in the cell, not per-case.
		for _, s := range cr.Samples {
			if s.Err == nil {
				st.latencies = append(st.latencies, s.TTFT)
			}
		}
		// Escalation is reported by the Runner directly; inferring it from
		// Runs+Errors breaks under a custom MinRuns.
		if cr.Escalated {
			st.escalated = append(st.escalated, cr.CaseID)
		}
		for _, ar := range cr.Asserts {
			if ar.Pass {
				st.successes++
			}
			totalGraderErrors += ar.GraderErrors
			// Graded == 0 means the assertion never ran, distinct from failing.
			if ar.Graded == 0 {
				line := "[" + CellLabel(cr) + "] " + cr.CaseID + "/" + ar.Label
				if ar.FirstGraderError != "" {
					line += " — " + ar.FirstGraderError
				}
				unevaluated = append(unevaluated, line)
			}
		}
	}

	stw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintf(stw, "CELL\tRUNS\tERRORS\tSUCCESSES\tP50 LATENCY\tESCALATED PAST %d RUNS\n", defaultMinRuns); err != nil {
		return err
	}
	for _, c := range cells {
		st := stats[c]
		latency := formatLatency(p50Duration(st.latencies), len(st.latencies))
		if _, err := fmt.Fprintf(stw, "%s\t%d\t%d\t%d\t%s\t%s\n", c, st.runs, st.errors, st.successes, latency, joinOrNone(st.escalated)); err != nil {
			return err
		}
	}
	if err := stw.Flush(); err != nil {
		return err
	}

	errLabel := fmt.Sprintf("%d", totalErrors)
	if totalErrors > 0 {
		errLabel = colorize(ansiRed, errLabel)
	}
	if _, err := fmt.Fprintf(w, "\ntotal runs: %d, total errors: %s, grader errors: %d\n", totalRuns, errLabel, totalGraderErrors); err != nil {
		return err
	}
	if len(unevaluated) > 0 {
		if _, err := fmt.Fprintf(w, "%s\n", colorize(ansiYellow+ansiBold, "NEVER EVALUATED (0 graded samples, scored as failures):")); err != nil {
			return err
		}
		for _, line := range unevaluated {
			if _, err := fmt.Fprintf(w, "  %s\n", line); err != nil {
				return err
			}
		}
	}
	_, err := fmt.Fprintln(w, "caveat: per-case rates at N<=10 are triage, not results; "+
		"do not read a 7/10 vs 8/10 difference as a difference.")
	return err
}

// p50Duration: a single stalled call shouldn't skew a cell's reported latency.
func p50Duration(durs []time.Duration) time.Duration {
	if len(durs) == 0 {
		return 0
	}
	sorted := slices.Clone(durs)
	slices.Sort(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

func formatLatency(d time.Duration, n int) string {
	if n == 0 {
		return "n/a"
	}
	return d.Round(time.Millisecond).String()
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
