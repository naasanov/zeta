// This file is Part 4 of .docs/eval_harness_plan.md ("The variant axis"): the
// prompt-shape axis of the eval matrix, alongside the provider axis (live.go).
package eval

import (
	"fmt"
	"sort"
	"strings"

	"github.com/naasanov/zsh-autopilot/daemon/internal/prompt"
	"github.com/naasanov/zsh-autopilot/daemon/internal/protocol"
)

// FimCommentedHistoryVariant answers the plan doc's open question (b): does
// raw-history rendering (the current default, "fim-raw-history" — see
// DefaultVariant) actually beat the old commented form?
//
// # This does NOT do what the plan doc hypothesized — read before relying on it
//
// The plan doc's hypothesis was: build the Prompt normally, then nil out
// p.History, and RenderFIM (daemon/internal/provider/codestral.go) would fall
// back to rendering the "- recent commands: ..." Context line as a "#"
// shell comment, since (per the doc) RenderFIM "skips [it] only when History
// is populated".
//
// That is NOT what RenderFIM actually does. Reading it
// (daemon/internal/provider/codestral.go:174-185): the loop that turns
// p.Context lines into "#" comments skips any line matching
// prompt.RecentCommandsLabel UNCONDITIONALLY —
//
//	stripped := strings.TrimPrefix(line, "- ")
//	if strings.HasPrefix(stripped, prompt.RecentCommandsLabel) {
//		continue // <-- always taken for the recent-commands line, regardless
//	}           //     of whether p.History is populated
//
// — and separately, the raw-history loop just ranges over p.History. So:
//
//   - Prompt.Build populates BOTH p.Context (which contains the "- recent
//     commands: ..." line, since req.History was non-empty) AND p.History.
//   - Nilling p.History after Build only empties the raw-history loop.
//   - The Context line is dropped anyway, unconditionally, by RenderFIM.
//
// Net effect: this variant does not produce "commented history" — it
// produces NO history at all in the rendered FIM prompt. Reproducing this
// finding rather than working around it (production code, codestral.go, is
// out of this task's file lane): the case corpus's history-bearing cases
// (C1-C3, D1-D4, E1/E2/E6/E7, the FIM adapter under this variant) become a
// "no history" condition, not a "commented history" condition, until
// RenderFIM itself grows a seam for it (the plan doc's deferred
// WithFIMRenderer functional option, Part 4 "only if the ordering variant is
// in scope" — it is not, here).
//
// The variant is kept and named as specified, because it is still a real,
// useful condition to measure (does the model do worse/better/the same with
// NO history at all vs raw-history) — it is just not the condition the name
// suggests today. FimCommentedHistoryVariant's doc comment is the correction;
// do not remove this note without re-verifying RenderFIM's behavior.
func FimCommentedHistoryVariant() Variant {
	return Variant{
		Name: "fim-commented-history",
		Build: func(req protocol.Request) prompt.Prompt {
			p := prompt.Build(req)
			p.History = nil
			return p
		},
	}
}

// variantRegistry is the ordered, named set of variants VariantByName and
// AllVariants draw from. A slice (not a map) so AllVariants and error
// messages have a stable, deliberate order rather than Go's randomized map
// iteration.
var variantRegistry = []Variant{
	DefaultVariant(),
	FimCommentedHistoryVariant(),
}

// AllVariants returns every registered variant, in a stable order — the
// "-matrix" axis.
func AllVariants() []Variant {
	out := make([]Variant, len(variantRegistry))
	copy(out, variantRegistry)
	return out
}

// VariantByName looks up a variant by its exact Name, case-insensitively.
// An unknown name is a fatal-shaped error listing every valid name — the
// same "fail loudly on a typo" rule Select applies to case selectors: a
// -variants typo that quietly ran the default would silently narrow a
// matrix run to a single cell.
func VariantByName(name string) (Variant, error) {
	lower := strings.ToLower(strings.TrimSpace(name))
	for _, v := range variantRegistry {
		if strings.ToLower(v.Name) == lower {
			return v, nil
		}
	}
	names := make([]string, len(variantRegistry))
	for i, v := range variantRegistry {
		names[i] = v.Name
	}
	sort.Strings(names)
	return Variant{}, fmt.Errorf("eval: unknown variant %q, want one of %s", name, strings.Join(names, ", "))
}
