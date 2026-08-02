package provider

import (
	"strings"
	"testing"

	"github.com/naasanov/zsh-autopilot/daemon/internal/config"
	"github.com/naasanov/zsh-autopilot/daemon/internal/prompt"
)

func TestRenderChatPrompt(t *testing.T) {
	pl := prompt.ChatPayload{System: "sys text", User: "git sta"}
	got := RenderChatPrompt(pl)

	if !strings.Contains(got, "SYSTEM:\nsys text") {
		t.Errorf("RenderChatPrompt() = %q, want it to contain the rendered system block", got)
	}
	if !strings.Contains(got, "USER:\ngit sta") {
		t.Errorf("RenderChatPrompt() = %q, want it to contain USER:\\ngit sta", got)
	}
}

// TestNewFromProfile_ShapeMismatch asserts a clear, named error rather than a
// panic or silent misbehavior when a prompt doesn't match the adapter it's
// paired with — e.g. a FIM-only prompt handed to the anthropic (chat) adapter.
func TestNewFromProfile_ShapeMismatch(t *testing.T) {
	fimOnly, err := prompt.ByName("fim-no-marker")
	if err != nil {
		t.Fatalf("prompt.ByName(fim-no-marker): %v", err)
	}

	r := config.ResolvedProfile{Adapter: "anthropic", Model: "claude-test"}
	_, err = NewFromProfile(r, "test-key", 48, fimOnly)
	if err == nil {
		t.Fatal("NewFromProfile() err = nil, want non-nil for a FIM prompt on the anthropic adapter")
	}
	if !strings.Contains(err.Error(), "fim-no-marker") {
		t.Errorf("NewFromProfile() err = %q, want it to name the prompt %q", err.Error(), "fim-no-marker")
	}
	if !strings.Contains(err.Error(), "anthropic") {
		t.Errorf("NewFromProfile() err = %q, want it to name the adapter %q", err.Error(), "anthropic")
	}
}

// TestNewFromProfile_UnknownAdapter asserts an unrecognized adapter errors
// rather than panicking — config.Resolve should never produce one, but the
// switch must fail closed if it somehow does.
func TestNewFromProfile_UnknownAdapter(t *testing.T) {
	chatOnly, err := prompt.ByName("chat-append")
	if err != nil {
		t.Fatalf("prompt.ByName(chat-append): %v", err)
	}
	r := config.ResolvedProfile{Adapter: "bogus"}
	_, err = NewFromProfile(r, "test-key", 48, chatOnly)
	if err == nil {
		t.Fatal("NewFromProfile() err = nil, want non-nil for an unknown adapter")
	}
}
