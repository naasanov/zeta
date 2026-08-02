package prompt

import (
	"strings"
	"testing"
)

func TestByNameAndAll(t *testing.T) {
	wantNames := []string{
		"chat-append",
		"fim-transcript-marker",
		"fim-commented-history",
		"fim-exit-code-always",
		"fim-no-marker",
	}
	all := All()
	if len(all) != len(wantNames) {
		t.Fatalf("All() returned %d prompts, want %d", len(all), len(wantNames))
	}
	for i, p := range all {
		if p.Name() != wantNames[i] {
			t.Errorf("All()[%d].Name() = %q, want %q", i, p.Name(), wantNames[i])
		}
	}

	for _, name := range wantNames {
		if _, err := ByName(name); err != nil {
			t.Errorf("ByName(%q) error: %v", name, err)
		}
		// case-insensitive
		if _, err := ByName(strings.ToUpper(name)); err != nil {
			t.Errorf("ByName(%q) (uppercased) error: %v", name, err)
		}
	}

	if _, err := ByName("nonexistent"); err == nil {
		t.Error("ByName(\"nonexistent\") expected error, got nil")
	} else {
		for _, name := range wantNames {
			if !strings.Contains(err.Error(), name) {
				t.Errorf("ByName error %q does not list %q", err.Error(), name)
			}
		}
	}
}

func TestShippedFor(t *testing.T) {
	cases := map[string]string{
		"openai":    "chat-append",
		"anthropic": "chat-append",
		"codestral": "fim-transcript-marker",
	}
	for adapter, want := range cases {
		if got := ShippedFor(adapter).Name(); got != want {
			t.Errorf("ShippedFor(%q).Name() = %q, want %q", adapter, got, want)
		}
	}
}
