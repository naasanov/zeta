package prompt

import (
	"strings"
	"testing"

	"github.com/naasanov/zsh-autopilot/daemon/internal/protocol"
)

// TestContextBlockOmitsAbsentFields is the negative case: with no context
// fields set (the common case pre-step-5 and for a bare KindTyping request),
// contextBlock must produce nothing at all, so it's safe to prepend
// unconditionally in llmSuggest.
func TestContextBlockOmitsAbsentFields(t *testing.T) {
	got := contextBlock(protocol.Request{V: protocol.Version, ID: "x", Kind: protocol.KindTyping, Buf: "git ad"})
	if got != "" {
		t.Errorf("expected empty block for a request with no context fields, got %q", got)
	}
}

// TestContextBlockLastExitZeroOmitted checks the one deliberately asymmetric
// rule in the contract: LastExit == 0 (or absent, indistinguishable on the
// Go side once decoded) must NOT produce a "last command" line, since only
// failures are worth surfacing to the model.
func TestContextBlockLastExitZeroOmitted(t *testing.T) {
	got := contextBlock(protocol.Request{LastExit: 0, Cwd: "/tmp"})
	if strings.Contains(got, "last command") {
		t.Errorf("expected no last-command line for LastExit == 0, got %q", got)
	}
}

// TestContextBlockEmptyHistoryAndBranchOmitted checks that a present-but-empty
// slice/string doesn't leak a line either.
func TestContextBlockEmptyHistoryAndBranchOmitted(t *testing.T) {
	got := contextBlock(protocol.Request{Cwd: "/tmp", GitBranch: "", History: nil})
	if strings.Contains(got, "git:") {
		t.Errorf("expected no git line for empty GitBranch, got %q", got)
	}
	if strings.Contains(got, "recent commands") {
		t.Errorf("expected no recent-commands line for empty History, got %q", got)
	}
}

// TestContextBlockFullyPopulated is the positive case: every field present
// produces exactly the documented line, in order, with correct formatting
// (dirty suffix, exit code, semicolon-joined history oldest-to-newest).
func TestContextBlockFullyPopulated(t *testing.T) {
	req := protocol.Request{
		V: protocol.Version, ID: "sess.1", Kind: protocol.KindTyping, Buf: "git ad",
		Cwd:       "/Users/x/project",
		GitBranch: "main",
		GitDirty:  true,
		LastExit:  1,
		History:   []string{"cd project", "npm install", "npm test"},
	}
	got := contextBlock(req)

	wantLines := []string{
		"Context:",
		"- cwd: /Users/x/project",
		"- git: branch main (dirty)",
		"- last command failed (exit 1)",
		"- recent commands: cd project; npm install; npm test",
	}
	for _, want := range wantLines {
		if !strings.Contains(got, want) {
			t.Errorf("expected block to contain %q, got:\n%s", want, got)
		}
	}
	// Order matters for the stable-prefix caching story (design §7): cwd,
	// then git, then last-exit, then history.
	idxs := make([]int, len(wantLines)-1)
	for i, want := range wantLines[1:] {
		idxs[i] = strings.Index(got, want)
		if idxs[i] < 0 {
			t.Fatalf("line %q missing from block", want)
		}
	}
	for i := 1; i < len(idxs); i++ {
		if idxs[i] < idxs[i-1] {
			t.Errorf("context lines out of order: %v", wantLines[1:])
		}
	}
}

// TestContextBlockCleanBranchNoDirtySuffix ensures a clean tree doesn't get
// the "(dirty)" suffix appended.
func TestContextBlockCleanBranchNoDirtySuffix(t *testing.T) {
	got := contextBlock(protocol.Request{GitBranch: "main", GitDirty: false})
	if !strings.Contains(got, "- git: branch main\n") && !strings.HasSuffix(strings.TrimSpace(got), "- git: branch main") {
		t.Errorf("expected clean branch line without (dirty) suffix, got %q", got)
	}
	if strings.Contains(got, "(dirty)") {
		t.Errorf("did not expect (dirty) suffix on a clean tree, got %q", got)
	}
}

// TestBufferStaysLastInAssembledUserMessage asserts the load-bearing
// invariant from the task contract: however much context is prepended, the
// KindTyping user message must end with req.Buf so the model's completion
// continues directly from it (the zsh client strips this exact prefix).
func TestBufferStaysLastInAssembledUserMessage(t *testing.T) {
	req := protocol.Request{
		Kind: protocol.KindTyping,
		Buf:  "git ad",
		Cwd:  "/Users/x/project", GitBranch: "main", GitDirty: true, LastExit: 1,
		History: []string{"cd project", "npm install", "npm test"},
	}
	_, user := Build(req)
	if !strings.HasSuffix(user, req.Buf) {
		t.Errorf("expected assembled user message to end with req.Buf %q, got:\n%s", req.Buf, user)
	}
}

// TestPromptUsesOneSystemPromptForBothModes pins the cleanup that makes
// next-command prediction the same append contract as typing completion, just
// with an empty buffer and a different user-turn directive.
func TestPromptUsesOneSystemPromptForBothModes(t *testing.T) {
	typingSystem, typingUser := Build(protocol.Request{Kind: protocol.KindTyping, Buf: "git ad"})
	nextSystem, nextUser := Build(protocol.Request{Kind: protocol.KindNextCommand, Buf: ""})

	if typingSystem != systemPrompt {
		t.Errorf("typing request used unexpected system prompt")
	}
	if nextSystem != systemPrompt {
		t.Errorf("next-command request used unexpected system prompt")
	}
	if typingSystem != nextSystem {
		t.Errorf("expected typing and next-command to share one system prompt")
	}
	if !strings.Contains(typingUser, "Complete this command") {
		t.Errorf("typing user prompt missing typing directive, got:\n%s", typingUser)
	}
	if !strings.Contains(nextUser, "next command") {
		t.Errorf("next-command user prompt missing next-command directive, got:\n%s", nextUser)
	}
}

// TestNextCommandPromptCarriesContextAndNoFakeBufferMarker checks that
// next-command mode keeps using the real request context but no longer needs a
// special "(prompt is empty)" sentinel in the user turn.
func TestNextCommandPromptCarriesContextAndNoFakeBufferMarker(t *testing.T) {
	req := protocol.Request{
		Kind:      protocol.KindNextCommand,
		Buf:       "",
		Cwd:       "/Users/x/project",
		GitBranch: "main",
		History:   []string{"npm test"},
	}
	_, user := Build(req)

	for _, want := range []string{
		"Context:",
		"- cwd: /Users/x/project",
		"- git: branch main",
		"- recent commands: npm test",
		"next command",
	} {
		if !strings.Contains(user, want) {
			t.Errorf("expected next-command prompt to contain %q, got:\n%s", want, user)
		}
	}
	if strings.Contains(user, "(prompt is empty)") {
		t.Errorf("did not expect legacy empty-prompt sentinel, got:\n%s", user)
	}
}
