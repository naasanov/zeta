package eval

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

// Select filters cases down to those matching a comma-separated,
// case-insensitive selector list (case ID, category, or glob on the ID),
// preserving corpus order. A selector matching no case is a fatal error.
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
	// A malformed pattern (e.g. unclosed "[") is treated as "matches
	// nothing", surfacing alongside ordinary typos in the unmatched error.
	ok, err := path.Match(sel, strings.ToLower(c.ID))
	return err == nil && ok
}

// splitSelectors splits a comma-separated selector list, trimming whitespace,
// lowercasing, and dropping empties.
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

// knownIDs lists the corpus's case IDs for the error message, sorted.
func knownIDs(cases []Case) []string {
	ids := make([]string, 0, len(cases))
	for _, c := range cases {
		ids = append(ids, c.ID)
	}
	sort.Strings(ids)
	return ids
}
