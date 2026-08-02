// Comparing two scorecard runs: `go run ./cmd/eval -diff old.json new.json`
// answers "did that change help or hurt?" without eyeballing two tables.
//
// Deliberately does NOT require the two runs to share a case set (the corpus
// grows over time) or report every per-case rate wobble as a "change" — at
// N<=10, per-case rates are triage, not results (see noiseFloorPP).
package eval

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"text/tabwriter"
)

// Run is a scorecard dump, loaded back from what report.go's JSON wrote.
type Run struct {
	Meta    Meta
	Results []CaseResult
}

// runDump mirrors the unexported `dump` struct JSON encodes in report.go
// field-for-field (same JSON tags), so LoadRun is JSON's exact inverse
// without report.go needing to export anything. JSON writes one document
// via a single Encoder.Encode call (not a stream), so a single
// json.Unmarshal is the correct inverse.
type runDump struct {
	Meta    Meta         `json:"meta"`
	Results []CaseResult `json:"results"`
}

// LoadRun reads a scorecard JSON dump written by JSON() (report.go).
//
// An empty or corrupt document is an error, never a zero-value Run: a diff
// against a silently-empty "before" run would report every current case as
// "new" and every current pass as an "improvement", which is a worse failure
// mode than refusing to diff at all.
func LoadRun(r io.Reader) (Run, error) {
	var d runDump
	dec := json.NewDecoder(r)
	if err := dec.Decode(&d); err != nil {
		return Run{}, fmt.Errorf("eval: LoadRun: %w", err)
	}
	return Run{Meta: d.Meta, Results: d.Results}, nil
}

// RowClass is how one (case, assertion) row compares across two runs.
type RowClass string

const (
	RowNew           RowClass = "new"            // absent in before
	RowRemoved       RowClass = "removed"        // absent in after
	RowNotComparable RowClass = "not-comparable" // Graded==0 on either side
	RowMeasure       RowClass = "measure"        // Polarity Measure: tracked, never a verdict
	RowImproved      RowClass = "improved"
	RowRegressed     RowClass = "regressed"
	RowUnchanged     RowClass = "unchanged" // includes noise-floor-suppressed deltas
)

// Flip records a categorical Pass/Fail transition. Empty string means no
// flip. Flips are categorical, not statistical: they bypass the noise floor
// entirely, per the plan doc ("status flips are the headline").
type Flip string

const (
	NoFlip         Flip = ""
	FlipPassToFail Flip = "PASS->FAIL"
	FlipFailToPass Flip = "FAIL->PASS"
)

// RowDiff is one (CaseID, Label) comparison.
type RowDiff struct {
	CaseID   string
	Category string
	Label    string
	Polarity Polarity

	Class RowClass

	// Before/After are nil when the row is RowNew or RowRemoved respectively.
	Before *AssertionResult
	After  *AssertionResult

	// DeltaPP is After's rate minus Before's rate, in percentage points.
	// Only meaningful when Class is not RowNew/RowRemoved/RowNotComparable.
	DeltaPP float64

	// NoiseFloorPP is the floor that was applied to this row (0 for rows
	// where a floor doesn't apply: New/Removed/NotComparable/Measure, and
	// rows resolved by a categorical Flip instead).
	NoiseFloorPP float64
	// BelowNoiseFloor marks a real-but-small delta that was deliberately
	// reported as unchanged because it's statistically indistinguishable
	// from sampling noise at this N. Kept visible (rather than silently
	// folded into RowUnchanged) so the report can call out that this was a
	// deliberate suppression, not "nothing happened".
	BelowNoiseFloor bool

	// Flip is set whenever Pass toggled between runs — for TripWire
	// assertions this is a trip/untrip transition. Flips are categorical:
	// they are reported and count toward Regressed() regardless of N or
	// rate magnitude.
	Flip Flip

	// TripWireNewlyTripped is Flip==FlipPassToFail && Polarity==TripWire,
	// pulled out as its own bool because it's the loudest thing in the
	// report and Text/Regressed both need to test for it directly.
	TripWireNewlyTripped bool
	// TripWireOffending is After's FirstOffending, populated only when
	// TripWireNewlyTripped so the report can print the offending output
	// inline (a trip-wire hit is meaningless without seeing what tripped it).
	TripWireOffending string
}

// CategoryAggregate is a suite-level (or per-category) pass-rate summary: the
// fraction of assertions PASSING, not a Present/Graded ratio — Must and
// MustNot can't be summed on a common "presence rate", but each assertion's
// own polarity-normalized Pass bool can. Measure assertions are excluded
// (always Pass=true, so including them would inflate the rate with vacuous
// passes).
//
// Before/After are computed independently from each Run's own results, not
// matched (case,label) pairs, so a category's rate stays meaningful even
// when the two runs' case sets differ.
type CategoryAggregate struct {
	Category                  string // "" / "ALL" for the suite-wide aggregate
	BeforeTotal, BeforePassed int
	AfterTotal, AfterPassed   int
	BeforeRate, AfterRate     float64 // fraction in [0,1]; 0 when Total==0 (see Text's "n/a" handling)
	DeltaPP                   float64 // AfterRate-BeforeRate in points; meaningful only when both totals>0
}

// DiffReport is the result of comparing two runs.
type DiffReport struct {
	Before, After Meta
	// MetaWarnings lists every Meta field that differs between the two runs
	// (provider/model/variant/n-policy). Non-empty means the two runs may
	// not be an apples-to-apples comparison (e.g. codestral vs anthropic) —
	// still a legitimate diff to run, but one that must not print silently.
	MetaWarnings []string

	Suite      CategoryAggregate   // Category == "ALL"
	Categories []CategoryAggregate // in first-appearance order (Before's, then After's)

	// Rows is every (CaseID, Label) union across both runs, in
	// category-then-case-then-assertion order (After's ordering, with
	// Before-only cases/labels appended after what's already been seen).
	Rows []RowDiff
}

// noiseFloorPP is the minimum |delta| (percentage points) treated as a real
// change for one (case, assertion) rate comparison, rather than sampling
// noise. It's 2×SE at p=0.5 (the maximally noisy true rate), evaluated at
// the SMALLER of the two runs' Graded counts. At N=10 (this harness's
// sampling cap) that's ~31.6pp — deliberately conservative, so a smaller
// per-N floor still compares well-saturated cases at their own precision.
func noiseFloorPP(n int) float64 {
	if n <= 0 {
		return 100 // nothing clears this; callers route Graded==0 to "not comparable" before reaching here anyway
	}
	se := math.Sqrt(0.25 / float64(n))
	return 200 * se // 2*SE, expressed in percentage points (SE is a fraction of 1.0)
}

// rowKey identifies one (case, assertion) row.
type rowKey struct{ caseID, label string }

// caseAsserts flattens a Run's results into per-row lookups plus the
// ordering metadata Text needs: which category each case belongs to, and in
// what order cases/labels first appeared.
type caseAsserts struct {
	byRow      map[rowKey]*AssertionResult
	caseOrder  []string
	category   map[string]string
	labelOrder map[string][]string
}

func flatten(results []CaseResult) caseAsserts {
	ca := caseAsserts{
		byRow:      map[rowKey]*AssertionResult{},
		category:   map[string]string{},
		labelOrder: map[string][]string{},
	}
	seenCase := map[string]bool{}
	for i := range results {
		cr := &results[i]
		if !seenCase[cr.CaseID] {
			seenCase[cr.CaseID] = true
			ca.caseOrder = append(ca.caseOrder, cr.CaseID)
			ca.category[cr.CaseID] = cr.Category
		}
		for j := range cr.Asserts {
			ar := &cr.Asserts[j]
			ca.byRow[rowKey{cr.CaseID, ar.Label}] = ar
			ca.labelOrder[cr.CaseID] = append(ca.labelOrder[cr.CaseID], ar.Label)
		}
	}
	return ca
}

// unionOrder returns a's elements in order, followed by any of b's elements
// not already in a. Used to build a stable row order across two runs whose
// case/assertion sets may only partially overlap.
func unionOrder(a, b []string) []string {
	seen := make(map[string]bool, len(a))
	out := make([]string, 0, len(a)+len(b))
	for _, x := range a {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	for _, x := range b {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// passCounts sums Pass across every non-Measure assertion in results,
// optionally restricted to one category ("" means all categories).
func passCounts(results []CaseResult, category string) (total, passed int) {
	for _, cr := range results {
		if category != "" && cr.Category != category {
			continue
		}
		for _, ar := range cr.Asserts {
			if ar.Polarity == Measure {
				continue
			}
			total++
			if ar.Pass {
				passed++
			}
		}
	}
	return total, passed
}

func aggregate(category string, before, after []CaseResult) CategoryAggregate {
	bt, bp := passCounts(before, category)
	at, ap := passCounts(after, category)
	agg := CategoryAggregate{
		Category:    category,
		BeforeTotal: bt, BeforePassed: bp,
		AfterTotal: at, AfterPassed: ap,
	}
	if bt > 0 {
		agg.BeforeRate = float64(bp) / float64(bt)
	}
	if at > 0 {
		agg.AfterRate = float64(ap) / float64(at)
	}
	if bt > 0 && at > 0 {
		agg.DeltaPP = 100 * (agg.AfterRate - agg.BeforeRate)
	}
	return agg
}

// metaWarnings lists which Meta fields differ between two runs. Timestamp is
// deliberately excluded — two runs always have different timestamps, and
// that's not the kind of mismatch this is warning about.
func metaWarnings(before, after Meta) []string {
	var warns []string
	if before.Provider != after.Provider {
		warns = append(warns, fmt.Sprintf("provider differs: %s vs %s", orNA(before.Provider), orNA(after.Provider)))
	}
	if before.Model != after.Model {
		warns = append(warns, fmt.Sprintf("model differs: %s vs %s", orNA(before.Model), orNA(after.Model)))
	}
	if before.Variant != after.Variant {
		warns = append(warns, fmt.Sprintf("variant differs: %s vs %s", orNA(before.Variant), orNA(after.Variant)))
	}
	if before.NPolicy != after.NPolicy {
		warns = append(warns, fmt.Sprintf("n-policy differs: %s vs %s", orNA(before.NPolicy), orNA(after.NPolicy)))
	}
	return warns
}

// DiffRuns compares two runs, keyed by (CaseID, assertion Label).
func DiffRuns(before, after Run) DiffReport {
	bIdx := flatten(before.Results)
	aIdx := flatten(after.Results)

	rep := DiffReport{
		Before:       before.Meta,
		After:        after.Meta,
		MetaWarnings: metaWarnings(before.Meta, after.Meta),
		Suite:        aggregate("", before.Results, after.Results),
	}
	rep.Suite.Category = "ALL"

	catOrder := unionOrder(categoryOrder(before.Results), categoryOrder(after.Results))
	for _, cat := range catOrder {
		rep.Categories = append(rep.Categories, aggregate(cat, before.Results, after.Results))
	}

	caseOrder := unionOrder(aIdx.caseOrder, bIdx.caseOrder)
	for _, caseID := range caseOrder {
		labels := unionOrder(aIdx.labelOrder[caseID], bIdx.labelOrder[caseID])
		category := aIdx.category[caseID]
		if category == "" {
			category = bIdx.category[caseID]
		}
		for _, label := range labels {
			key := rowKey{caseID, label}
			rep.Rows = append(rep.Rows, buildRow(caseID, category, label, bIdx.byRow[key], aIdx.byRow[key]))
		}
	}

	return rep
}

func categoryOrder(results []CaseResult) []string {
	seen := map[string]bool{}
	var order []string
	for _, cr := range results {
		if !seen[cr.Category] {
			seen[cr.Category] = true
			order = append(order, cr.Category)
		}
	}
	return order
}

func buildRow(caseID, category, label string, before, after *AssertionResult) RowDiff {
	row := RowDiff{CaseID: caseID, Category: category, Label: label}

	switch {
	case after == nil:
		row.Polarity = before.Polarity
		row.Class = RowRemoved
		row.Before = before
		return row
	case before == nil:
		row.Polarity = after.Polarity
		row.Class = RowNew
		row.After = after
		return row
	}

	row.Polarity = after.Polarity
	row.Before = before
	row.After = after

	// Graded==0 on either side is "not comparable, never an improvement or
	// regression" (plan doc constraint carried over from report.go's own
	// "0 of 0 must never read as a pass" rule) — checked before flip/tripwire
	// detection so an unevaluated assertion (Pass forced false by runner.go)
	// can never masquerade as a FAIL->PASS or PASS->FAIL transition.
	if before.Graded == 0 || after.Graded == 0 {
		row.Class = RowNotComparable
		return row
	}

	beforeRate := 100 * float64(before.Present) / float64(before.Graded)
	afterRate := 100 * float64(after.Present) / float64(after.Graded)
	row.DeltaPP = afterRate - beforeRate

	if row.Polarity == Measure {
		row.Class = RowMeasure
		return row
	}

	if before.Pass != after.Pass {
		if after.Pass {
			row.Flip = FlipFailToPass
			row.Class = RowImproved
		} else {
			row.Flip = FlipPassToFail
			row.Class = RowRegressed
		}
		if row.Polarity == TripWire && row.Flip == FlipPassToFail {
			row.TripWireNewlyTripped = true
			row.TripWireOffending = after.FirstOffending
		}
		return row
	}

	// Pass matches on both sides. TripWire has no rate/threshold concept
	// beyond Pass itself (it's a zero-tolerance trip-wire, not a percentage,
	// per the plan doc), so an unchanged Pass is simply unchanged — no
	// noise floor needed or meaningful.
	if row.Polarity == TripWire {
		row.Class = RowUnchanged
		return row
	}

	floor := noiseFloorPP(min(before.Graded, after.Graded))
	row.NoiseFloorPP = floor
	if math.Abs(row.DeltaPP) < floor {
		row.Class = RowUnchanged
		row.BelowNoiseFloor = true
		return row
	}

	sign := 1.0
	if row.Polarity == MustNot {
		sign = -1.0 // for MustNot, a LOWER rate is the improvement
	}
	if row.DeltaPP*sign > 0 {
		row.Class = RowImproved
	} else {
		row.Class = RowRegressed
	}
	return row
}

// Regressed reports whether anything got worse, for use as a process exit
// code. Deliberately conservative: keys off categorical signals (a status
// flip to FAIL, a trip-wire newly tripped) and suite-level movement past the
// noise floor, never an individual noisy per-case rate delta.
func (d DiffReport) Regressed() bool {
	for _, row := range d.Rows {
		if row.Flip == FlipPassToFail {
			return true
		}
		if row.TripWireNewlyTripped {
			return true
		}
	}
	if d.Suite.BeforeTotal > 0 && d.Suite.AfterTotal > 0 {
		floor := noiseFloorPP(min(d.Suite.BeforeTotal, d.Suite.AfterTotal))
		if d.Suite.DeltaPP < -floor {
			return true
		}
	}
	return false
}

// Text renders the human-readable diff. Order is deliberate: trip-wire
// transitions first (loudest — a regression in shipped logic, not a quality
// judgment), then the suite aggregate (the plan doc: "the suite aggregate is
// where decisions get made"), then per-category, then the full row table,
// then a footer explaining the noise floor and listing new/removed cases.
func (d DiffReport) Text(w io.Writer) error {
	if err := d.writeHeader(w); err != nil {
		return err
	}
	if err := d.writeTripWireTransitions(w); err != nil {
		return err
	}
	if err := d.writeStatusFlips(w); err != nil {
		return err
	}
	if err := d.writeSuite(w); err != nil {
		return err
	}
	if err := d.writeCategories(w); err != nil {
		return err
	}
	if err := d.writeRows(w); err != nil {
		return err
	}
	return d.writeFooter(w)
}

func (d DiffReport) writeHeader(w io.Writer) error {
	if _, err := fmt.Fprintf(w, "eval diff: [%s/%s/%s] -> [%s/%s/%s]\n",
		orNA(d.Before.Provider), orNA(d.Before.Model), orNA(d.Before.Variant),
		orNA(d.After.Provider), orNA(d.After.Model), orNA(d.After.Variant)); err != nil {
		return err
	}
	if len(d.MetaWarnings) > 0 {
		if _, err := fmt.Fprintln(w, "WARNING: comparing runs with different metadata — this may not be apples-to-apples:"); err != nil {
			return err
		}
		for _, warn := range d.MetaWarnings {
			if _, err := fmt.Fprintf(w, "  - %s\n", warn); err != nil {
				return err
			}
		}
	}
	_, err := fmt.Fprintln(w)
	return err
}

func (d DiffReport) writeTripWireTransitions(w io.Writer) error {
	var newlyTripped, fixed []RowDiff
	for _, row := range d.Rows {
		if row.Polarity != TripWire || row.Flip == NoFlip {
			continue
		}
		if row.TripWireNewlyTripped {
			newlyTripped = append(newlyTripped, row)
		} else {
			fixed = append(fixed, row)
		}
	}
	if len(newlyTripped) == 0 && len(fixed) == 0 {
		_, err := fmt.Fprintln(w, "TRIP WIRES: no new transitions.")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(w)
		return err
	}
	if len(newlyTripped) > 0 {
		if _, err := fmt.Fprintf(w, "!!! TRIP WIRES NEWLY TRIPPED (%d) — regressions in shipped logic, not quality judgments:\n", len(newlyTripped)); err != nil {
			return err
		}
		for _, row := range newlyTripped {
			if _, err := fmt.Fprintf(w, "  [%s] %s: offending output = %q\n", row.CaseID, row.Label, row.TripWireOffending); err != nil {
				return err
			}
		}
	}
	if len(fixed) > 0 {
		if _, err := fmt.Fprintf(w, "trip wires newly CLEAN (%d):\n", len(fixed)); err != nil {
			return err
		}
		for _, row := range fixed {
			if _, err := fmt.Fprintf(w, "  [%s] %s\n", row.CaseID, row.Label); err != nil {
				return err
			}
		}
	}
	_, err := fmt.Fprintln(w)
	return err
}

func (d DiffReport) writeStatusFlips(w io.Writer) error {
	var flips []RowDiff
	for _, row := range d.Rows {
		if row.Flip == NoFlip || row.Polarity == TripWire {
			continue // trip-wire flips already shown, louder, above
		}
		flips = append(flips, row)
	}
	if len(flips) == 0 {
		_, err := fmt.Fprintln(w, "STATUS FLIPS: none.")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(w)
		return err
	}
	if _, err := fmt.Fprintf(w, "STATUS FLIPS (%d) — categorical, bypass the noise floor:\n", len(flips)); err != nil {
		return err
	}
	for _, row := range flips {
		if _, err := fmt.Fprintf(w, "  [%s] %s: %s (%d/%d -> %d/%d)\n",
			row.CaseID, row.Label, row.Flip,
			row.Before.Present, row.Before.Graded, row.After.Present, row.After.Graded); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(w)
	return err
}

func fmtRate(rate float64, total int) string {
	if total == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.0f%%", 100*rate)
}

func (d DiffReport) writeSuite(w io.Writer) error {
	_, err := fmt.Fprintf(w, "SUITE PASS RATE: %s (%d/%d) -> %s (%d/%d)  delta %+.1fpp\n\n",
		fmtRate(d.Suite.BeforeRate, d.Suite.BeforeTotal), d.Suite.BeforePassed, d.Suite.BeforeTotal,
		fmtRate(d.Suite.AfterRate, d.Suite.AfterTotal), d.Suite.AfterPassed, d.Suite.AfterTotal,
		d.Suite.DeltaPP)
	return err
}

func (d DiffReport) writeCategories(w io.Writer) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "CATEGORY\tBEFORE\tAFTER\tDELTA"); err != nil {
		return err
	}
	for _, cat := range d.Categories {
		if _, err := fmt.Fprintf(tw, "%s\t%s (%d/%d)\t%s (%d/%d)\t%+.1fpp\n",
			cat.Category,
			fmtRate(cat.BeforeRate, cat.BeforeTotal), cat.BeforePassed, cat.BeforeTotal,
			fmtRate(cat.AfterRate, cat.AfterTotal), cat.AfterPassed, cat.AfterTotal,
			cat.DeltaPP); err != nil {
			return err
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	_, err := fmt.Fprintln(w)
	return err
}

func (d DiffReport) writeRows(w io.Writer) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "CATEGORY\tCASE\tASSERTION\tPOL\tBEFORE\tAFTER\tDELTA\tCLASS"); err != nil {
		return err
	}
	for _, row := range d.Rows {
		before, after := "-", "-"
		if row.Before != nil {
			before = fmt.Sprintf("%d/%d", row.Before.Present, row.Before.Graded)
		}
		if row.After != nil {
			after = fmt.Sprintf("%d/%d", row.After.Present, row.After.Graded)
		}
		delta := "-"
		class := string(row.Class)
		switch row.Class {
		case RowNew, RowRemoved:
			// delta stays "-": there is nothing to subtract from/to.
		case RowNotComparable:
			delta = "n/a"
		default:
			delta = fmt.Sprintf("%+.1fpp", row.DeltaPP)
			if row.BelowNoiseFloor {
				class = fmt.Sprintf("%s (N below noise floor, +/-%.0fpp)", class, row.NoiseFloorPP)
			}
			if row.Flip != NoFlip {
				class = fmt.Sprintf("%s [%s]", class, row.Flip)
			}
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			row.Category, row.CaseID, row.Label, row.Polarity, before, after, delta, class); err != nil {
			return err
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	_, err := fmt.Fprintln(w)
	return err
}

func (d DiffReport) writeFooter(w io.Writer) error {
	var newRows, removedRows, notComparable []string
	for _, row := range d.Rows {
		id := row.CaseID + "/" + row.Label
		switch row.Class {
		case RowNew:
			newRows = append(newRows, id)
		case RowRemoved:
			removedRows = append(removedRows, id)
		case RowNotComparable:
			notComparable = append(notComparable, id)
		}
	}
	if _, err := fmt.Fprintf(w, "new: %s\n", joinOrNone(newRows)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "removed: %s\n", joinOrNone(removedRows)); err != nil {
		return err
	}
	if len(notComparable) > 0 {
		if _, err := fmt.Fprintf(w, "not comparable (0 graded on one side): %s\n", joinOrNone(notComparable)); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(w, "caveat: per-case rate deltas below the row's noise floor are reported "+
		"unchanged on purpose — at N<=10 a small wobble is triage, not a result; only status flips and "+
		"trip-wire transitions bypass that floor, and the suite pass rate above is what decisions should be made on.")
	return err
}
