package eval

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

// wantIDs is the full case set the plan doc's "Test cases" tables list:
// categories A, B, D, F1/F2 in full, plus every C and E case — the
// deterministic ones from Part 2 and the three Part 3 judged cases
// (C3, E3, F2).
var wantIDs = []string{
	"A1", "A2", "A3", "A4", "A5", "A6", "A7", "A8", "A9",
	"B1", "B2", "B3", "B3b", "B4", "B5", "B6", "B6b", "B7", "B7b", "B8", "B8b",
	"C1", "C2", "C3",
	"D1", "D2", "D3", "D4",
	"E1", "E2", "E3", "E4", "E5", "E6", "E8", "E9", "E10", "E11", "E12", "E13", "E14",
	"F1", "F2",
}

func TestCases_ExactIDSet(t *testing.T) {
	cases := Cases()

	got := make([]string, len(cases))
	for i, c := range cases {
		got[i] = c.ID
	}
	sort.Strings(got)

	want := append([]string(nil), wantIDs...)
	sort.Strings(want)

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Cases() ID set mismatch.\n got:  %v\nwant: %v", got, want)
	}
}

func TestCases_JudgedIDsUseJudgeGrader(t *testing.T) {
	// Checks both directions against Cases(), so the judged set and the
	// actual judge-graded cases can't silently drift apart. A judged case
	// may also pair the judge assertion with deterministic ones (E11 keeps
	// its deterministic "stale-history-wins" MustNot alongside the judged leg).
	judged := map[string]bool{"C3": true, "E3": true, "E10": true, "E11": true, "E14": true, "F2": true}
	for _, c := range Cases() {
		hasJudgeGrader := false
		for _, a := range c.Asserts {
			if a.Polarity != Measure && strings.HasPrefix(a.Grader.Name(), "judge:") {
				hasJudgeGrader = true
			}
		}
		if judged[c.ID] && !hasJudgeGrader {
			t.Errorf("case %s is in the judged set but has no judge.go grader (name prefix \"judge:\") among its assertions", c.ID)
		}
		if !judged[c.ID] && hasJudgeGrader {
			t.Errorf("case %s has a judge.go grader but is missing from the judged set in this test", c.ID)
		}
	}
}

func TestCases_WellFormed(t *testing.T) {
	cases := Cases()
	if len(cases) == 0 {
		t.Fatal("Cases() returned no cases")
	}

	seen := map[string]bool{}
	for _, c := range cases {
		t.Run(c.ID, func(t *testing.T) {
			if c.ID == "" {
				t.Error("case has empty ID")
			}
			if seen[c.ID] {
				t.Errorf("duplicate case ID %q", c.ID)
			}
			seen[c.ID] = true

			if c.Category == "" {
				t.Errorf("case %s has empty category", c.ID)
			}
			if len(c.Asserts) == 0 {
				t.Errorf("case %s has zero assertions", c.ID)
			}

			for _, a := range c.Asserts {
				if a.Grader == nil {
					t.Errorf("case %s assertion %q has nil Grader", c.ID, a.Label)
				}
				if a.Label == "" {
					t.Errorf("case %s has an assertion with empty Label", c.ID)
				}
				switch a.Polarity {
				case Must, MustNot:
					if a.Threshold <= 0 || a.Threshold > 1 {
						t.Errorf("case %s assertion %q (%s) has out-of-range threshold %v; want (0,1]",
							c.ID, a.Label, a.Polarity, a.Threshold)
					}
				case TripWire, Measure:
					// Threshold is ignored for these polarities per
					// Assertion's doc comment; no constraint to check.
				default:
					t.Errorf("case %s assertion %q has unrecognized polarity %q", c.ID, a.Label, a.Polarity)
				}
			}
		})
	}
}

func TestCases_CategoriesMatchPlan(t *testing.T) {
	// The plan doc names these five category strings; a typo'd category
	// silently breaks -cases <category> selection (select.go) and the
	// report's category grouping (report.go).
	wantCategories := map[string]bool{
		"syntax": true, "fabrication": true, "incrementing": true,
		"looping": true, "context": true, "abstention": true,
	}
	for _, c := range Cases() {
		if !wantCategories[c.Category] {
			t.Errorf("case %s has unexpected category %q", c.ID, c.Category)
		}
	}
}

// caseShape is the structural, comparable projection of a Case used by
// TestCases_Deterministic. A Grader is a func-carrying interface
// (GraderFunc.F), and reflect.DeepEqual on funcs is only ever true for two
// nils — even functionally-identical closures built by two separate
// Cases() calls compare unequal. So determinism is checked over everything
// EXCEPT the grader funcs themselves: ID, category, request, and each
// assertion's label/polarity/threshold/grader name.
type caseShape struct {
	ID, Category string
	Req          interface{}
	Asserts      []assertShape
}

type assertShape struct {
	Label      string
	Polarity   Polarity
	Threshold  float64
	GraderName string
}

func shapeOf(cases []Case) []caseShape {
	out := make([]caseShape, len(cases))
	for i, c := range cases {
		asserts := make([]assertShape, len(c.Asserts))
		for j, a := range c.Asserts {
			asserts[j] = assertShape{a.Label, a.Polarity, a.Threshold, a.Grader.Name()}
		}
		out[i] = caseShape{c.ID, c.Category, c.Req, asserts}
	}
	return out
}

func TestCases_Deterministic(t *testing.T) {
	a := shapeOf(Cases())
	b := shapeOf(Cases())
	if !reflect.DeepEqual(a, b) {
		t.Fatal("Cases() is not deterministic across calls")
	}
}

// TestCases_SelectableByCategoryAndGlob is a light integration check that
// the corpus actually composes with Select (select.go) the way cmd/eval
// drives it — the exact failure mode this Part 2 work needs to avoid is a
// corpus that looks right in isolation but a selector like "syntax" or "B*"
// silently matches nothing against it.
func TestCases_SelectableByCategoryAndGlob(t *testing.T) {
	cases := Cases()

	byCategory, err := Select(cases, "syntax")
	if err != nil {
		t.Fatalf("Select(syntax): %v", err)
	}
	if len(byCategory) != 9 {
		t.Errorf("Select(syntax) matched %d cases, want 9 (A1..A9)", len(byCategory))
	}

	byGlob, err := Select(cases, "B*")
	if err != nil {
		t.Fatalf("Select(B*): %v", err)
	}
	if len(byGlob) != 12 {
		t.Errorf("Select(B*) matched %d cases, want 12 (B1..B8 plus the four paired \"b\" cases)", len(byGlob))
	}

	// "B6" must select the bare case ALONE — a selector that silently swept
	// in its paired high-context sibling would make the two indistinguishable
	// in a scorecard, which is the whole point of the pairing. "B6*" is the
	// selector that takes both.
	bare, err := Select(cases, "B6")
	if err != nil {
		t.Fatalf("Select(B6): %v", err)
	}
	if len(bare) != 1 || bare[0].ID != "B6" {
		t.Errorf("Select(B6) matched %d cases, want exactly B6", len(bare))
	}
	pair, err := Select(cases, "B6*")
	if err != nil {
		t.Fatalf("Select(B6*): %v", err)
	}
	if len(pair) != 2 {
		t.Errorf("Select(B6*) matched %d cases, want 2 (B6 and B6b)", len(pair))
	}

	if _, err := Select(cases, "NOPE"); err == nil {
		t.Error("Select(NOPE) should have failed loudly, matched nothing silently instead")
	}
}
