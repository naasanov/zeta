package main

import (
	"testing"

	"github.com/naasanov/zsh-autopilot/daemon/internal/config"
	"github.com/naasanov/zsh-autopilot/daemon/internal/prompt"
)

func TestResolveCellsAcceptsProfileName(t *testing.T) {
	cfg := config.Config{
		Profiles: map[string]config.Profile{
			"my-codestral": {Provider: "codestral"},
		},
	}

	cells, skipped := resolveCells(false, false, "my-codestral", "default", cfg)
	if len(skipped) != 0 {
		t.Fatalf("resolveCells() skipped = %v, want none", skipped)
	}
	if len(cells) != 1 {
		t.Fatalf("resolveCells() cells = %v, want exactly one", cells)
	}
	if cells[0].Provider != "my-codestral" {
		t.Errorf("cells[0].Provider = %q, want the profile name my-codestral", cells[0].Provider)
	}
	want := prompt.ShippedFor("codestral").Name()
	if cells[0].PromptName != want {
		t.Errorf("cells[0].PromptName = %q, want %q", cells[0].PromptName, want)
	}
}
