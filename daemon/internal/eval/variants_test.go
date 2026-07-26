package eval

import (
	"strings"
	"testing"

	"github.com/naasanov/zsh-autopilot/daemon/internal/protocol"
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
