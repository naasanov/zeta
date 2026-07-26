package eval

import (
	"strings"
	"testing"
)

func selectCorpus() []Case {
	return []Case{
		{ID: "A1", Category: "syntax"},
		{ID: "A2", Category: "syntax"},
		{ID: "A10", Category: "syntax"},
		{ID: "B1", Category: "fabrication"},
		{ID: "B2", Category: "fabrication"},
		{ID: "E7", Category: "context"},
	}
}

func ids(cases []Case) string {
	out := make([]string, len(cases))
	for i, c := range cases {
		out[i] = c.ID
	}
	return strings.Join(out, ",")
}

func TestSelect(t *testing.T) {
	tests := []struct {
		name      string
		selectors string
		want      string
	}{
		{"empty returns everything", "", "A1,A2,A10,B1,B2,E7"},
		{"single id", "A1", "A1"},
		{"explicit list", "A1,B2", "A1,B2"},
		{"whitespace and trailing comma tolerated", " A1 , B2 , ", "A1,B2"},
		{"case-insensitive id", "a1", "A1"},
		{"category", "syntax", "A1,A2,A10"},
		{"category is case-insensitive", "FABRICATION", "B1,B2"},
		{"glob", "a*", "A1,A2,A10"},
		{"glob single char does not match A10", "a?", "A1,A2"},
		{"mixed forms", "syntax,B1", "A1,A2,A10,B1"},
		{"overlapping selectors dedupe", "A1,a*,syntax", "A1,A2,A10"},
		{"corpus order is preserved, not selector order", "E7,A1", "A1,E7"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Select(selectCorpus(), tt.selectors)
			if err != nil {
				t.Fatalf("Select(%q) err = %v, want nil", tt.selectors, err)
			}
			if ids(got) != tt.want {
				t.Errorf("Select(%q) = %s, want %s", tt.selectors, ids(got), tt.want)
			}
		})
	}
}

// TestSelect_UnmatchedSelectorIsAnError is the point of the whole function:
// a selector that matches nothing must fail loudly. Silently running the
// subset that did match would exit 0 with a clean scorecard for a run that
// skipped the case the user was actually asking about.
func TestSelect_UnmatchedSelectorIsAnError(t *testing.T) {
	for _, sel := range []string{
		"A11",     // plausible typo for A1 that must not silently match A1
		"A1,NOPE", // one good selector must not excuse a bad one
		"z*",      // glob matching nothing
		"a[",      // malformed glob
		"syntaxx", // near-miss category
	} {
		t.Run(sel, func(t *testing.T) {
			_, err := Select(selectCorpus(), sel)
			if err == nil {
				t.Fatalf("Select(%q) err = nil, want an error naming the unmatched selector", sel)
			}
			if !strings.Contains(err.Error(), "known cases:") {
				t.Errorf("Select(%q) error %q does not list the known case IDs to correct against", sel, err)
			}
		})
	}
}

// TestSelect_PartialMatchStillErrors pins that a valid selector alongside an
// invalid one fails rather than quietly running the valid half.
func TestSelect_PartialMatchStillErrors(t *testing.T) {
	_, err := Select(selectCorpus(), "A1,B9")
	if err == nil {
		t.Fatal("Select(\"A1,B9\") err = nil, want an error; B9 matches nothing")
	}
	if !strings.Contains(err.Error(), `"B9"`) && !strings.Contains(err.Error(), `"b9"`) {
		t.Errorf("error %q should name the offending selector B9", err)
	}
}
