// This file is Part 4 of .docs/eval_harness_plan.md ("The variant axis"): the
// prompt-shape axis of the eval matrix, alongside the provider axis (live.go).
package eval

import (
	"fmt"
	"sort"
	"strings"

	"github.com/naasanov/zsh-autopilot/daemon/internal/prompt"
	"github.com/naasanov/zsh-autopilot/daemon/internal/protocol"
	"github.com/naasanov/zsh-autopilot/daemon/internal/provider"
)

// FimCommentedHistoryVariant despite its name does NOT render commented
// history: RenderFIM drops the "- recent commands: ..." Context line
// unconditionally (codestral.go), so nilling p.History here yields NO
// history at all, not a commented form of it. It's kept under this name
// anyway because "no history vs. raw-history" is still a useful condition to
// measure. Producing actual commented history needs a RenderFIM seam
// (WithFIMRenderer) that doesn't exist yet.
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

// FimNoPromptMarkerVariant renders the pre-marker shape (raw history, no
// "$ " transcript marker) so the marker's win over this baseline stays
// re-measurable instead of asserted. Changes the renderer, not Build, so
// it's only a distinct condition in a codestral cell (see Variant.FIMRenderer).
func FimNoPromptMarkerVariant() Variant {
	return Variant{
		Name:        "fim-no-prompt-marker",
		Build:       prompt.Build,
		FIMRenderer: provider.RenderFIMNoPromptMarker,
	}
}

// FimExitCodeAlwaysVariant emits an "# exit: N" line between the history
// block and the cursor even when N is 0, on top of the shipped marker shape.
// Lost as a boundary fix to the marker, but stays registered to ask a
// separate question: does explicit exit status help once the boundary
// problem is already solved, worth its per-request token cost?
func FimExitCodeAlwaysVariant() Variant {
	return Variant{
		Name:        "fim-exit-code-always",
		Build:       prompt.Build,
		FIMRenderer: provider.RenderFIMExitCodeAlways,
	}
}

// variantRegistry is the ordered, named set of variants VariantByName and
// AllVariants draw from. A slice (not a map) so AllVariants and error
// messages have a stable, deliberate order rather than Go's randomized map
// iteration.
var variantRegistry = []Variant{
	DefaultVariant(),
	FimCommentedHistoryVariant(),
	FimNoPromptMarkerVariant(),
	FimExitCodeAlwaysVariant(),
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
