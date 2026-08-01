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

// ANSI codes for the report. Kept to pass/fail signal only, gated on
// NO_COLOR (https://no-color.org) — any non-empty value disables color.
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

// colorize wraps s in the given ANSI code(s), unless NO_COLOR is set or code
// is empty (nothing to highlight).
func colorize(code, s string) string {
	if code == "" || noColor() {
		return s
	}
	return code + s + ansiReset
}

// ColorizeSymbol wraps a CaseSymbol glyph in the same color the scorecard
// gives it, honoring NO_COLOR — exported so a live progress row (e.g. the
// eval CLI's stderr stream, which prints CaseSymbol output directly rather
// than through Text) can match the report's color semantics without
// duplicating the ANSI codes or the NO_COLOR gate.
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

// ColorGood/ColorBad/ColorWarn wrap s in the report's green/red/yellow,
// honoring NO_COLOR — exported for the same reason as ColorizeSymbol.
func ColorGood(s string) string { return colorize(ansiGreen, s) }
func ColorBad(s string) string  { return colorize(ansiRed, s) }
func ColorWarn(s string) string { return colorize(ansiYellow, s) }

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

// PrettyJSON writes the same document JSON writes, but easier to eyeball in
// a terminal: embedded newlines in string fields (chiefly CaseResult.Prompt)
// render as literal newlines instead of JSON's required "\n" escape, and
// HTML-sensitive characters (<, >, &, common in shell commands like
// "git push && git status") are left unescaped instead of becoming <
// etc. (encoding/json's default, meant for embedding JSON in HTML — not a
// concern in a terminal).
//
// The output is NOT valid JSON: -diff/LoadRun must always read what JSON()
// wrote, never this. The newline rendering works by a blunt find-and-replace
// of the two-byte "\n" sequence in the encoded bytes, which is safe for the
// overwhelming majority of this corpus (shell commands and prompt text) but
// has one known blind spot: a string containing the two literal characters
// backslash-n (e.g. a prompt that itself quotes a regex like '\n') encodes
// as the four-byte "\\n" and would have its trailing "\n" portion misread as
// an escaped newline too. A token-aware unescape would avoid that at the
// cost of real machinery this is a viewer convenience, not a parser.
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
		cell, caseID, label, offending string
	}
	var hits []hit
	for _, cr := range results {
		for _, ar := range cr.Asserts {
			if ar.Polarity == TripWire && !ar.Pass {
				// The cell label is part of the identity: "A2 tripped" in a
				// matrix run is not actionable until you know WHICH provider
				// tripped it, and a wire that trips on one provider but not
				// another is a different bug from one that trips on all.
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

// CellLabel identifies the (provider, variant) cell a CaseResult belongs to.
// It is the scorecard's column key — the plan doc's deliverable is
// "case × (provider × prompt-variant) → pass-rate", so a report that renders
// results as a flat list is unreadable the moment there is more than one
// cell: the same case ID appears once per cell with nothing distinguishing
// the rows, which is exactly the comparison the harness exists to produce.
func CellLabel(cr CaseResult) string {
	provider := orNA(cr.Provider)
	if cr.Variant == "" {
		return provider
	}
	return provider + "/" + cr.Variant
}

// assertionKey identifies one scorecard ROW: a (case, assertion) pair, which
// is compared across every cell column.
type assertionKey struct {
	Category string
	CaseID   string
	Label    string
}

// writeScorecard renders the pivot: one row per (case, assertion), one column
// per cell, so cells are read side by side rather than as repeated blocks.
//
// Each cell renders as "<symbol> <rate>" — symbol first, because a column of
// failures should be visible by shape before any number is read, and it keeps
// the glyphs consistent with the live progress row (see CaseSymbol). k/N stays
// in the JSON dump for the diff; putting it in every cell here would triple
// the width of a 6-column matrix for information the rate already conveys.
func writeScorecard(w io.Writer, results []CaseResult) error {
	cells := orderedCells(results)
	rows, byCellRow := scorecardRows(results)

	// Rendered into a buffer, not w directly: tabwriter sizes columns from
	// raw byte length, so an ANSI-colored cell would look "wider" than a
	// plain one and throw off alignment. cellVerdict emits same-width
	// placeholder bytes instead of the real glyphs; decorateGlyphs swaps
	// them for (optionally colored) glyphs AFTER Flush, once alignment is
	// already fixed in stone and invisible escape bytes can't shift it.
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
				// This cell never produced this (case, assertion) at all —
				// distinct from a failure, and worth showing as a hole rather
				// than an implied pass.
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

// Placeholder bytes cellVerdict emits in place of the real pass/fail/
// tripwire/error glyphs, swapped for (optionally colored) glyphs by
// decorateGlyphs after tabwriter has already fixed column widths. Control
// bytes, so they can't collide with real case/category text.
const (
	glyphPassPH byte = '\x01'
	glyphFailPH byte = '\x02'
	glyphTripPH byte = '\x03'
	glyphErrPH  byte = '\x04'
)

// decorateGlyphs replaces glyph placeholder bytes with their real —
// optionally colored — glyph, after tabwriter has already computed and
// applied column padding from the placeholders' plain byte widths.
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

// cellVerdict renders one cell of the pivot: a glyph plus the measured rate.
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

// orderedCells lists the distinct cell labels in first-seen order, so column
// order matches the order the cells were actually run rather than map order.
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

// scorecardRows returns the row keys in first-seen order (grouped by the
// order categories first appear) plus a cell→row→result lookup for the pivot.
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
	// Per-cell sampling summary. A single flat "escalated: A3" list across a
	// matrix run is ambiguous and, worse, prints the same case ID once per
	// cell — the old footer rendered "A1, A2, ... A1, A2, ..." for two cells,
	// which reads as a bug in the corpus rather than two cells' worth of
	// results.
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
		// Pooled across every case in the cell, not per-case: the cell is the
		// unit of comparison here (this provider/variant tuple vs. that one),
		// and a per-case breakdown can be added later if it's ever needed.
		for _, s := range cr.Samples {
			if s.Err == nil {
				st.latencies = append(st.latencies, s.TTFT)
			}
		}
		// Escalation is reported by the Runner, not inferred from
		// Runs+Errors: that inference breaks the moment MinRuns is
		// customised or a case errors out before reaching the threshold.
		if cr.Escalated {
			st.escalated = append(st.escalated, cr.CaseID)
		}
		for _, ar := range cr.Asserts {
			if ar.Pass {
				st.successes++
			}
			totalGraderErrors += ar.GraderErrors
			// Graded == 0 means the assertion never actually ran. It is
			// scored as a failure, but it needs calling out separately —
			// "failed" and "never evaluated" demand different fixes. Append
			// the first grader error's message, if any, so "NEVER EVALUATED"
			// is diagnosable from the report alone rather than requiring a
			// re-run under a debugger — this is what a bad judge model id
			// (a 404 that reads like an auth failure) previously hid.
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

// p50Duration returns the median of durs. It's the report's stated latency
// stat rather than mean: a single stalled call (cold connection, transient
// rate limit) shouldn't be able to make a cell look slower than the latency
// most of its runs actually experienced — exactly the eval-environment noise
// a provider/variant tradeoff comparison should not be skewed by.
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

// formatLatency renders a P50 duration for the footer table; "n/a" when no
// successful sample carried a TTFT (all errored, or a stub/test result that
// never set one) so a real zero doesn't need to be distinguished from "no
// data" by the reader.
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
