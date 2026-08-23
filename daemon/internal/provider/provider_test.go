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

// TestNewFromProfile_MaxTokensOverride asserts a non-zero ResolvedProfile.MaxTokens
// (e.g. the groq preset's gpt-oss-20b override) wins over the caller's global
// maxTokens param — see the doc comment on NewFromProfile.
func TestNewFromProfile_MaxTokensOverride(t *testing.T) {
	chatOnly, err := prompt.ByName("chat-append")
	if err != nil {
		t.Fatalf("prompt.ByName(chat-append): %v", err)
	}

	r := config.ResolvedProfile{Adapter: "openai", Model: "openai/gpt-oss-20b", MaxTokens: 150}
	p, err := NewFromProfile(r, "test-key", 48, chatOnly)
	if err != nil {
		t.Fatalf("NewFromProfile() err = %v, want nil", err)
	}
	c, ok := p.(*openAIClient)
	if !ok {
		t.Fatalf("NewFromProfile() returned %T, want *openAIClient", p)
	}
	if c.maxTokens != 150 {
		t.Errorf("maxTokens = %v, want override 150, not the caller's 48", c.maxTokens)
	}
}

// TestNewFromProfile_MaxTokensFallsBackToParam asserts a zero
// ResolvedProfile.MaxTokens (the common case) leaves the caller's own
// maxTokens param untouched.
func TestNewFromProfile_MaxTokensFallsBackToParam(t *testing.T) {
	chatOnly, err := prompt.ByName("chat-append")
	if err != nil {
		t.Fatalf("prompt.ByName(chat-append): %v", err)
	}

	r := config.ResolvedProfile{Adapter: "openai", Model: "codestral-latest"}
	p, err := NewFromProfile(r, "test-key", 48, chatOnly)
	if err != nil {
		t.Fatalf("NewFromProfile() err = %v, want nil", err)
	}
	c, ok := p.(*openAIClient)
	if !ok {
		t.Fatalf("NewFromProfile() returned %T, want *openAIClient", p)
	}
	if c.maxTokens != 48 {
		t.Errorf("maxTokens = %v, want the caller's param 48", c.maxTokens)
	}
}
