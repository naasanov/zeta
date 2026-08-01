package eval

import (
	"strings"
	"testing"

	"github.com/naasanov/zsh-autopilot/daemon/internal/prompt"
	"github.com/naasanov/zsh-autopilot/daemon/internal/protocol"
	"github.com/naasanov/zsh-autopilot/daemon/internal/provider"
)

func TestDefaultVariant_Name(t *testing.T) {
	v := DefaultVariant()
	if v.Name != "default" {
		t.Errorf("DefaultVariant().Name = %q, want %q", v.Name, "default")
	}
}

func TestFimCommentedHistoryVariant_NilsHistory(t *testing.T) {
	req := protocol.Request{
		Kind:    protocol.KindNextCommand,
		History: []string{"git add .", `git commit -m "wip"`},
		Cwd:     "/x/project",
	}

	v := FimCommentedHistoryVariant()
	if v.Name != "fim-commented-history" {
		t.Fatalf("Name = %q, want fim-commented-history", v.Name)
	}

	p := v.Build(req)
	if p.History != nil {
		t.Errorf("Build(req).History = %v, want nil", p.History)
	}
	// The Context block itself is unaffected by the variant (it's rendered
	// from req.History before the mutation), so the recent-commands line is
	// still present there.
	if !strings.Contains(p.Context, "recent commands") {
		t.Errorf("Context = %q, want it to still contain the recent-commands line (built before History was nil'd)", p.Context)
	}
}

// TestFimCommentedHistoryVariant_ActuallyDropsHistoryFromFIM locks in the
// documented finding: RenderFIM (codestral.go) drops the recent-commands
// Context line UNCONDITIONALLY, not only when Prompt.History is populated.
// So this variant does not resurrect history as a "#" comment — it produces
// NO history in the rendered FIM prompt at all. This test guards against
// RenderFIM's behavior silently changing out from under variants.go's doc
// comment (which explains this at length) without anyone noticing.
func TestFimCommentedHistoryVariant_ActuallyDropsHistoryFromFIM(t *testing.T) {
	req := protocol.Request{
		Kind:    protocol.KindNextCommand,
		History: []string{"git add .", `git commit -m "wip"`},
	}

	base := DefaultVariant().Build(req)
	commented := FimCommentedHistoryVariant().Build(req)

	// Sanity: the two variants render identical Context (RenderFIM strips the
	// recent-commands line from Context regardless of which variant built the
	// Prompt), and differ only in the History field.
	if base.Context != commented.Context {
		t.Errorf("Context differs between variants: base=%q commented=%q", base.Context, commented.Context)
	}
	if len(base.History) == 0 {
		t.Fatal("test setup: base.History should be populated")
	}
	if commented.History != nil {
		t.Errorf("commented.History = %v, want nil", commented.History)
	}
}

// TestShapeVariants_SetRendererNotBuild pins what makes a shape variant a
// different KIND of variant: it leaves prompt content alone (Build ==
// prompt.Build) and changes only the FIM rendering. A shape variant that
// accidentally mutated Build too would confound the very comparison it
// exists to make.
func TestShapeVariants_SetRendererNotBuild(t *testing.T) {
	req := protocol.Request{Kind: protocol.KindNextCommand, History: []string{"git status", "git push"}}
	base := prompt.Build(req)

	for _, v := range []Variant{FimNoPromptMarkerVariant(), FimExitCodeAlwaysVariant()} {
		if v.FIMRenderer == nil {
			t.Errorf("variant %q: FIMRenderer is nil, want a renderer", v.Name)
			continue
		}
		got := v.Build(req)
		if got.Prefix != base.Prefix || len(got.History) != len(base.History) || got.Context != base.Context {
			t.Errorf("variant %q: Build mutated the Prompt; shape variants must only change rendering", v.Name)
		}
	}
}

// TestShapeVariants_RenderDistinctPrompts is the guard that these variants
// are actually distinguishable conditions in a run: if two registry entries
// rendered identically, the scorecard would show two columns of the same
// experiment and read as a reproducibility check rather than a comparison.
func TestShapeVariants_RenderDistinctPrompts(t *testing.T) {
	p := prompt.Build(protocol.Request{Kind: protocol.KindNextCommand, History: []string{"git status", "git push"}})

	def, _ := provider.RenderFIM(p)
	noMarker, _ := FimNoPromptMarkerVariant().FIMRenderer(p)
	exit, _ := FimExitCodeAlwaysVariant().FIMRenderer(p)

	if noMarker == def {
		t.Error("fim-no-prompt-marker renders identically to the default shape")
	}
	if exit == def {
		t.Error("fim-exit-code-always renders identically to the default shape")
	}
	if noMarker == exit {
		t.Error("the two shape variants render identically to each other")
	}

	// The A9 fix, now shipped: the default must NOT end right after the last
	// history line. The baseline variant is expected to still do so — that is
	// what makes it the baseline.
	if strings.HasSuffix(def, "git push\n") {
		t.Errorf("default shape regressed to the A9 shape: %q", def)
	}
	if !strings.HasSuffix(noMarker, "git push\n") {
		t.Errorf("fim-no-prompt-marker should reproduce the pre-A9 shape, got %q", noMarker)
	}
	if strings.HasSuffix(exit, "git push\n") {
		t.Errorf("fim-exit-code-always still ends right after the last history line: %q", exit)
	}
}

// TestDefaultVariant_HasNoRenderer keeps "default" meaning the SHIPPED
// rendering path — a renderer here would make every baseline number in every
// past report incomparable to future ones.
func TestDefaultVariant_HasNoRenderer(t *testing.T) {
	if DefaultVariant().FIMRenderer != nil {
		t.Error("DefaultVariant must not override the FIM renderer")
	}
	if FimCommentedHistoryVariant().FIMRenderer != nil {
		t.Error("FimCommentedHistoryVariant is a Build-only variant; it must not set a renderer")
	}
}

func TestVariantByName(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
		wantV   string
	}{
		{"default", false, "default"},
		{"DEFAULT", false, "default"},
		{" default ", false, "default"},
		{"fim-commented-history", false, "fim-commented-history"},
		{"fim-no-prompt-marker", false, "fim-no-prompt-marker"},
		{"fim-exit-code-always", false, "fim-exit-code-always"},
		{"nope", true, ""},
		{"", true, ""},
	}
	for _, tt := range tests {
		v, err := VariantByName(tt.name)
		if tt.wantErr {
			if err == nil {
				t.Errorf("VariantByName(%q) = nil error, want an error", tt.name)
			}
			continue
		}
		if err != nil {
			t.Errorf("VariantByName(%q) unexpected error: %v", tt.name, err)
			continue
		}
		if v.Name != tt.wantV {
			t.Errorf("VariantByName(%q).Name = %q, want %q", tt.name, v.Name, tt.wantV)
		}
	}
}

func TestVariantByName_ErrorListsKnownNames(t *testing.T) {
	_, err := VariantByName("nope")
	if err == nil {
		t.Fatal("expected error")
	}
	for _, v := range AllVariants() {
		if !strings.Contains(err.Error(), v.Name) {
			t.Errorf("error %q does not mention known variant %q", err.Error(), v.Name)
		}
	}
}

func TestAllVariants(t *testing.T) {
	vs := AllVariants()
	if len(vs) < 2 {
		t.Fatalf("AllVariants() = %d variants, want at least 2 (default, fim-commented-history)", len(vs))
	}
	names := make(map[string]bool)
	for _, v := range vs {
		if v.Build == nil {
			t.Errorf("variant %q has a nil Build func", v.Name)
		}
		names[v.Name] = true
	}
	if !names["default"] || !names["fim-commented-history"] {
		t.Errorf("AllVariants() = %v, missing expected names", names)
	}

	// AllVariants returns a copy each call, so callers can't corrupt the
	// registry by mutating what they got back.
	vs[0].Name = "corrupted"
	if AllVariants()[0].Name == "corrupted" {
		t.Error("AllVariants() returned a slice that aliases the internal registry")
	}
}
