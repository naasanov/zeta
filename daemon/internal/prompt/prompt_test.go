package prompt

import (
	"fmt"
	"strings"
	"testing"

	"github.com/naasanov/zsh-autopilot/daemon/internal/protocol"
)

// TestContextBlockOmitsAbsentFields: with no context fields set,
// contextBlock must produce nothing at all.
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
	got := contextBlock(protocol.Request{Cwd: "/tmp", GitBranch: "", History: nil, DirEntries: nil})
	if strings.Contains(got, "git:") {
		t.Errorf("expected no git line for empty GitBranch, got %q", got)
	}
	if strings.Contains(got, "recent commands") {
		t.Errorf("expected no recent-commands line for empty History, got %q", got)
	}
	if strings.Contains(got, "files:") {
		t.Errorf("expected no files line for empty DirEntries, got %q", got)
	}
}

// TestContextBlockDirEntriesPresentAndAbsent checks the dir_entries render
// contract directly: present entries produce a "- files: ..." line
// space-joined (mirroring the history line's gating idiom), and an
// absent/empty slice produces no line at all.
func TestContextBlockDirEntriesPresentAndAbsent(t *testing.T) {
	got := contextBlock(protocol.Request{Cwd: "/tmp", DirEntries: []string{"a", "b", "c"}})
	if !strings.Contains(got, "- files: a b c") {
		t.Errorf("expected files line for present DirEntries, got %q", got)
	}

	got = contextBlock(protocol.Request{Cwd: "/tmp", DirEntries: []string{}})
	if strings.Contains(got, "files:") {
		t.Errorf("expected no files line for empty (non-nil) DirEntries, got %q", got)
	}

	got = contextBlock(protocol.Request{Cwd: "/tmp"})
	if strings.Contains(got, "files:") {
		t.Errorf("expected no files line for absent DirEntries, got %q", got)
	}
}

// TestContextBlockFullyPopulated is the positive case: every field present
// produces exactly the documented line, in order, with correct formatting
// (dirty suffix, exit code, semicolon-joined history oldest-to-newest).
func TestContextBlockFullyPopulated(t *testing.T) {
	req := protocol.Request{
		V: protocol.Version, ID: "sess.1", Kind: protocol.KindTyping, Buf: "git ad",
		Cwd:        "/Users/x/project",
		GitBranch:  "main",
		GitDirty:   true,
		LastExit:   1,
		History:    []string{"cd project", "npm install", "npm test"},
		DirEntries: []string{"README.md", "src", "go.mod"},
	}
	got := contextBlock(req)

	wantLines := []string{
		"Context:",
		"- cwd: /Users/x/project",
		"- files: README.md src go.mod",
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
	// then files (directory-static context, §7), then git, then last-exit,
	// then history.
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

// TestBufferStaysLastInAssembledUserMessage: however much context is
// prepended, the user message must end with req.Buf so the completion
// continues directly from it.
func TestBufferStaysLastInAssembledUserMessage(t *testing.T) {
	req := protocol.Request{
		Kind: protocol.KindTyping,
		Buf:  "git ad",
		Cwd:  "/Users/x/project", GitBranch: "main", GitDirty: true, LastExit: 1,
		History: []string{"cd project", "npm install", "npm test"},
	}
	user := chatAppend.RenderChat(req).User
	if !strings.HasSuffix(user, req.Buf) {
		t.Errorf("expected assembled user message to end with req.Buf %q, got:\n%s", req.Buf, user)
	}
}

// TestPromptUsesOneSystemPromptForBothModes checks that typing and
// next-command share one system prompt, differing only in the user-turn
// directive.
func TestPromptUsesOneSystemPromptForBothModes(t *testing.T) {
	typingPayload := chatAppend.RenderChat(protocol.Request{Kind: protocol.KindTyping, Buf: "git ad"})
	nextPayload := chatAppend.RenderChat(protocol.Request{Kind: protocol.KindNextCommand, Buf: ""})

	if typingPayload.System != chatSystemPrompt {
		t.Errorf("typing request used unexpected system prompt")
	}
	if nextPayload.System != chatSystemPrompt {
		t.Errorf("next-command request used unexpected system prompt")
	}
	if typingPayload.System != nextPayload.System {
		t.Errorf("expected typing and next-command to share one system prompt")
	}
	if !strings.Contains(typingPayload.User, "Complete this command") {
		t.Errorf("typing user prompt missing typing directive, got:\n%s", typingPayload.User)
	}
	if !strings.Contains(nextPayload.User, "next command") {
		t.Errorf("next-command user prompt missing next-command directive, got:\n%s", nextPayload.User)
	}
}

// TestFIMPopulatesHistory asserts the FIM prompts render req.History
// verbatim (oldest-first) as raw command lines, which is what makes the
// transcript contiguous with the buffer.
func TestFIMPopulatesHistory(t *testing.T) {
	req := protocol.Request{
		Kind:    protocol.KindTyping,
		Buf:     "git com",
		History: []string{"git add .", "git commit -m \"wip\"", "git status"},
	}
	got := fimTranscriptMarker.RenderFIM(req).Prefix
	for _, want := range []string{"$ git add .", "$ git commit -m \"wip\"", "$ git status"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected FIM prefix to contain %q, got:\n%s", want, got)
		}
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
	user := chatAppend.RenderChat(req).User

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

// TestFIMGuardCommentShape pins the two things the guard block can break:
// its last line must not run into the ambient context (the `{{if}}` vs
// `{{- if}}` trap noted on the template), and the rules must stay ABOVE the
// transcript so history and the buffer remain adjacent.
func TestFIMGuardCommentShape(t *testing.T) {
	req := protocol.Request{
		Kind:    protocol.KindTyping,
		Buf:     "git com",
		Cwd:     "/Users/x/project",
		History: []string{"git add .", "git status"},
	}
	got := fimGuardComment.RenderFIM(req).Prefix

	if !strings.Contains(got, "\n# cwd: /Users/x/project\n") {
		t.Errorf("cwd line not on its own line (guard block ran into it), got:\n%s", got)
	}
	if !strings.HasSuffix(got, "$ git status\n$ git com") {
		t.Errorf("history and buffer must stay contiguous at the end, got:\n%s", got)
	}
	rules := strings.Index(got, "# never invent a name")
	transcript := strings.Index(got, "$ git add .")
	if rules < 0 || transcript < 0 || rules > transcript {
		t.Errorf("rules block must precede the transcript, got:\n%s", got)
	}
}

// TestFIMGuardCommentWithoutContext covers the thin-context case the variant
// exists for: with no cwd/files/git, the rules block must still end cleanly
// before the transcript rather than merging into the first command line.
func TestFIMGuardCommentWithoutContext(t *testing.T) {
	req := protocol.Request{Kind: protocol.KindTyping, Buf: "ssh "}
	got := fimGuardComment.RenderFIM(req).Prefix
	if !strings.HasSuffix(got, "guess\n$ ssh ") {
		t.Errorf("rules block must end with a newline before the cursor line, got:\n%q", got)
	}
}

// ============================================================
// cwd-aware prompts: cwd-filtered, cwd-grouped (plan A.6b/A.6c/E)
// ============================================================

// TestCwdPromptsPassthroughWithoutCwdData is the control: whenever grouping
// cannot act (no req.Cwd, or no pool entry carries a known cwd), both
// cwd-filtered and cwd-grouped must render byte-identically to the
// baseline prompt, in both chat and FIM shape.
func TestCwdPromptsPassthroughWithoutCwdData(t *testing.T) {
	base := protocol.Request{
		Kind: protocol.KindTyping, Buf: "git com",
		Cwd: "/x/project", GitBranch: "main", LastExit: 1,
		DirEntries: []string{"README.md"},
	}

	noCwd := base
	noCwd.Cwd = ""
	noCwd.SetHistory(protocol.HistoryIn("/x/project", "git add .", "git status"))

	allUnknown := base
	allUnknown.SetHistory(protocol.HistoryUnknown("git add .", "git status"))

	noHistory := base

	cases := map[string]protocol.Request{
		"no cwd at all":                noCwd,
		"cwd set, history all unknown": allUnknown,
		"cwd set, no history at all":   noHistory,
	}

	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			wantChat := chatAppend.RenderChat(req)
			wantFIM := fimTranscriptMarker.RenderFIM(req)

			prompts := []interface {
				ChatPrompt
				FIMPrompt
			}{cwdFiltered, cwdGrouped}
			for _, p := range prompts {
				if got := p.RenderChat(req); got != wantChat {
					t.Errorf("%s.RenderChat passthrough mismatch:\ngot:  %+v\nwant: %+v", p.Name(), got, wantChat)
				}
				if got := p.RenderFIM(req); got != wantFIM {
					t.Errorf("%s.RenderFIM passthrough mismatch:\ngot:  %+v\nwant: %+v", p.Name(), got, wantFIM)
				}
			}
		})
	}
}

// TestBaselineTruncatesPoolIdenticallyToToday is the other control (A.6b): a
// pool larger than the rendered tail must render exactly what a pool that
// already IS that tail renders, for every prompt that truncates to
// defaultHistoryN.
func TestBaselineTruncatesPoolIdenticallyToToday(t *testing.T) {
	all100 := make([]string, 100)
	for i := range all100 {
		all100[i] = fmt.Sprintf("cmd-%02d", i)
	}
	tail30 := append([]string(nil), all100[len(all100)-defaultHistoryN:]...)

	pool := protocol.Request{Kind: protocol.KindTyping, Buf: "git com", Cwd: "/x/project", History: all100}
	short := protocol.Request{Kind: protocol.KindTyping, Buf: "git com", Cwd: "/x/project", History: tail30}

	if got, want := chatAppend.RenderChat(pool), chatAppend.RenderChat(short); got != want {
		t.Errorf("chat-append pool truncation mismatch:\ngot:  %+v\nwant: %+v", got, want)
	}

	fimPrompts := []FIMPrompt{fimTranscriptMarker, fimCommentedHistory, fimExitCodeAlways, fimNoMarker, fimGuardComment}
	for _, p := range fimPrompts {
		if got, want := p.RenderFIM(pool), p.RenderFIM(short); got != want {
			t.Errorf("%s pool truncation mismatch:\ngot:  %+v\nwant: %+v", p.Name(), got, want)
		}
	}
}

// TestCwdFilteredDropsOtherDirs checks that entries from a different
// directory never reach either rendering.
func TestCwdFilteredDropsOtherDirs(t *testing.T) {
	req := protocol.Request{Kind: protocol.KindTyping, Buf: "go bui", Cwd: "/proj"}
	req.SetHistory(
		protocol.HistoryIn("/proj", "proj-cmd-1", "proj-cmd-2"),
		protocol.HistoryIn("/other", "other-cmd-1", "other-cmd-2"),
	)

	fim := cwdFiltered.RenderFIM(req).Prefix
	for _, want := range []string{"$ proj-cmd-1", "$ proj-cmd-2"} {
		if !strings.Contains(fim, want) {
			t.Errorf("expected FIM prefix to contain %q, got:\n%s", want, fim)
		}
	}
	for _, notWant := range []string{"other-cmd-1", "other-cmd-2"} {
		if strings.Contains(fim, notWant) {
			t.Errorf("expected FIM prefix NOT to contain %q, got:\n%s", notWant, fim)
		}
	}

	chatUser := cwdFiltered.RenderChat(req).User
	if !strings.Contains(chatUser, "proj-cmd-1; proj-cmd-2") {
		t.Errorf("expected chat user to contain filtered history, got:\n%s", chatUser)
	}
	if strings.Contains(chatUser, "other-cmd") {
		t.Errorf("expected chat user NOT to contain other-dir commands, got:\n%s", chatUser)
	}
}

// TestCwdFilteredReachesRecalledSameDirEntries checks that cwd-filtered
// filters the full pool before truncating, so same-dir entries buried
// behind a large window of unrelated commands still surface (scenario 3,
// "cold / long absence", in the plan's coverage table).
func TestCwdFilteredReachesRecalledSameDirEntries(t *testing.T) {
	noise := make([]string, 40)
	for i := range noise {
		noise[i] = fmt.Sprintf("noise-%02d", i)
	}
	req := protocol.Request{Kind: protocol.KindTyping, Buf: "go bui", Cwd: "/proj"}
	req.SetHistory(
		protocol.HistoryIn("/proj", "old-proj-1", "old-proj-2"),
		protocol.HistoryUnknown(noise...),
	)

	fim := cwdFiltered.RenderFIM(req).Prefix
	for _, want := range []string{"$ old-proj-1", "$ old-proj-2"} {
		if !strings.Contains(fim, want) {
			t.Errorf("expected FIM prefix to contain recalled entry %q, got:\n%s", want, fim)
		}
	}
	if strings.Contains(fim, "noise-") {
		t.Errorf("expected FIM prefix NOT to contain unknown-cwd noise, got:\n%s", fim)
	}
}

// TestCwdGroupedRecallsSameDirEntriesBehindBootstrapWindow is the A.6b edge
// case: the pool's recent window is entirely cwd-unknown (bootstrap), with
// tagged same-dir entries only further back. cwd-grouped must still pull
// them forward, adjacent to the cursor, despite being oldest in the pool.
func TestCwdGroupedRecallsSameDirEntriesBehindBootstrapWindow(t *testing.T) {
	noise := make([]string, 40)
	for i := range noise {
		noise[i] = fmt.Sprintf("noise-%02d", i)
	}
	req := protocol.Request{Kind: protocol.KindTyping, Buf: "go bui", Cwd: "/proj"}
	req.SetHistory(
		protocol.HistoryIn("/proj", "old-proj-1", "old-proj-2"),
		protocol.HistoryUnknown(noise...),
	)

	fim := cwdGrouped.RenderFIM(req).Prefix
	if !strings.HasSuffix(fim, "$ old-proj-1\n$ old-proj-2\n$ go bui") {
		t.Errorf("expected recalled same-dir entries adjacent to cursor despite being oldest in the pool, got:\n%s", fim)
	}
}

// TestCwdGroupedPutsSameDirLast checks that same-dir entries render as a
// block immediately above the cursor, after any other-dir entries, and
// that the pool's chronological order is preserved within each group.
func TestCwdGroupedPutsSameDirLast(t *testing.T) {
	req := protocol.Request{Kind: protocol.KindTyping, Buf: "go bui", Cwd: "/proj"}
	req.SetHistory(
		protocol.HistoryIn("/proj", "proj-old"),
		protocol.HistoryIn("/other", "other-cmd"),
		protocol.HistoryIn("/proj", "proj-new"),
	)

	fim := cwdGrouped.RenderFIM(req).Prefix
	if !strings.HasSuffix(fim, "$ proj-old\n$ proj-new\n$ go bui") {
		t.Errorf("expected same-dir entries grouped last, adjacent to cursor, got:\n%s", fim)
	}
	otherIdx := strings.Index(fim, "other-cmd")
	sameIdx := strings.Index(fim, "proj-old")
	if otherIdx < 0 || sameIdx < 0 || otherIdx > sameIdx {
		t.Errorf("expected other-dir entries to render before the same-dir group, got:\n%s", fim)
	}

	chatUser := cwdGrouped.RenderChat(req).User
	earlierIdx := strings.Index(chatUser, "- earlier commands: other-cmd")
	sameDirIdx := strings.Index(chatUser, "- commands run in /proj: proj-old; proj-new")
	if earlierIdx < 0 || sameDirIdx < 0 || earlierIdx > sameDirIdx {
		t.Errorf("expected chat context to list earlier commands before the same-dir group, got:\n%s", chatUser)
	}
}

// TestCwdGroupedHeaderNeverAdjacentToCursor is the contiguity invariant: a
// group header renders only when its group is non-empty, so the line
// directly above the cursor is always a command, never a header.
func TestCwdGroupedHeaderNeverAdjacentToCursor(t *testing.T) {
	cases := []struct {
		name string
		segs []protocol.HistorySegment
	}{
		{"only other-dir", []protocol.HistorySegment{protocol.HistoryIn("/other", "o1", "o2")}},
		{"only same-dir", []protocol.HistorySegment{protocol.HistoryIn("/proj", "s1", "s2")}},
		{"both", []protocol.HistorySegment{protocol.HistoryIn("/other", "o1"), protocol.HistoryIn("/proj", "s1")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := protocol.Request{Kind: protocol.KindTyping, Buf: "go bui", Cwd: "/proj"}
			req.SetHistory(tc.segs...)

			fim := cwdGrouped.RenderFIM(req).Prefix
			lines := strings.Split(fim, "\n")
			cursorLine := lines[len(lines)-1]
			if !strings.HasPrefix(cursorLine, "$ go bui") {
				t.Fatalf("expected last line to be the cursor line, got %q in:\n%s", cursorLine, fim)
			}
			prev := lines[len(lines)-2]
			if !strings.HasPrefix(prev, "$ ") {
				t.Errorf("expected line above cursor to be a command, not a header, got %q in:\n%s", prev, fim)
			}
		})
	}
}

// TestCwdPromptsToleratesMisalignedCwds checks that HistoryCwds shorter,
// longer, or absent relative to History never panics either cwd-aware
// prompt, mirroring protocol.HistoryWithCwd's own tolerance.
func TestCwdPromptsToleratesMisalignedCwds(t *testing.T) {
	mk := func(history, cwds []string) protocol.Request {
		return protocol.Request{Kind: protocol.KindTyping, Buf: "go bui", Cwd: "/proj", History: history, HistoryCwds: cwds}
	}

	cases := []protocol.Request{
		mk([]string{"c0", "c1", "c2", "c3", "c4"}, []string{"/proj", "/proj"}),
		mk([]string{"c0", "c1", "c2", "c3", "c4"}, []string{"/proj", "/other", "/proj", "/other", "/x", "/y"}),
		mk([]string{"c0", "c1"}, nil),
	}

	for i, req := range cases {
		t.Run(fmt.Sprintf("case-%d", i), func(t *testing.T) {
			cwdFiltered.RenderFIM(req)
			cwdFiltered.RenderChat(req)
			cwdGrouped.RenderFIM(req)
			cwdGrouped.RenderChat(req)
		})
	}
}
