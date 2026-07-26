package eval

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

// Select filters cases down to those matching a comma-separated selector
// list, preserving the original corpus order (so a scorecard is diffable
// against a full run regardless of the order selectors were typed in).
//
// Each selector matches a case if any of the following hold, case-insensitively:
//
//   - it equals the case ID          — "A1"
//   - it equals the case category    — "syntax", "fabrication"
//   - it glob-matches the case ID    — "A*", "B?", "E[12]"
//
// The three forms overlap deliberately: "A1" is both an exact ID and a
// degenerate glob, and a category name is the ergonomic way to say "all the
// syntax trip-wires" without knowing which IDs exist today. Cases matched by
// more than one selector appear once.
//
// # Why every selector must match something
//
// Select returns an error naming any selector that matched no case, rather
// than silently returning the cases that did match. A typo'd selector that
// quietly narrows the run is the worst kind of eval bug: the harness exits 0,
// prints a clean scorecard, and the case you thought you were guarding was
// never executed. That is the same failure mode as an assertion with zero
// graded samples (see AssertionResult.GraderErrors) — "didn't run" must never
// be presentable as "fine".
func Select(cases []Case, selectors string) ([]Case, error) {
	sels := splitSelectors(selectors)
	if len(sels) == 0 {
		return cases, nil
	}

	matched := make(map[int]bool, len(cases))
	unmatched := make([]string, 0, len(sels))

	for _, sel := range sels {
		hit := false
		for i, c := range cases {
			if matchCase(c, sel) {
				matched[i] = true
				hit = true
			}
		}
		if !hit {
			unmatched = append(unmatched, sel)
		}
	}

	if len(unmatched) > 0 {
		return nil, fmt.Errorf("no case matches %s (known cases: %s)",
			strings.Join(quoteAll(unmatched), ", "), strings.Join(knownIDs(cases), ", "))
	}

	out := make([]Case, 0, len(matched))
	for i, c := range cases {
		if matched[i] {
			out = append(out, c)
		}
	}
	return out, nil
}

// matchCase reports whether sel selects c. sel is already lowercased and
// trimmed by splitSelectors.
func matchCase(c Case, sel string) bool {
	if strings.ToLower(c.ID) == sel || strings.ToLower(c.Category) == sel {
		return true
	}
	// path.Match only errors on a malformed pattern (e.g. an unclosed "["),
	// which we treat as "matches nothing" so the selector surfaces in the
	// unmatched error alongside ordinary typos, rather than as a separate
	// class of failure the caller has to handle differently.
	ok, err := path.Match(sel, strings.ToLower(c.ID))
	return err == nil && ok
}

// splitSelectors splits a comma-separated selector list, trimming whitespace,
// lowercasing, and dropping empties — so "A1, b*, " is three tokens, not four.
func splitSelectors(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.ToLower(strings.TrimSpace(part))
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func quoteAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = fmt.Sprintf("%q", s)
	}
	return out
}

// knownIDs lists the corpus's case IDs for the error message, sorted so the
// hint is stable and scannable when a selector typo needs correcting.
func knownIDs(cases []Case) []string {
	ids := make([]string, 0, len(cases))
	for _, c := range cases {
		ids = append(ids, c.ID)
	}
	sort.Strings(ids)
	return ids
}
