package prompt

import (
	"strings"
	"testing"

	"github.com/naasanov/zsh-autopilot/daemon/internal/protocol"
)

// TestGoldenRenderings pins every registered prompt's rendering byte for
// byte; a whitespace change here is a behavior change, not a style change.
// fim-commented-history has its own test below instead.
func TestGoldenRenderings(t *testing.T) {
	cases := []struct {
		name string
		req  protocol.Request

		wantChatSystem string
		wantChatUser   string

		wantMarkerPrefix   string
		wantNoMarkerPrefix string
		wantExitAlwaysPfx  string
	}{
		{
			name: "typing/full-context",
			req: protocol.Request{
				V: protocol.Version, ID: "1", Kind: protocol.KindTyping,
				Buf: "git com", Cwd: "/Users/x/project", GitBranch: "main", GitDirty: true, LastExit: 1,
				History:    []string{"git add .", "git commit -m \"wip\"", "git status"},
				DirEntries: []string{"README.md", "src", "tests"},
			},
			wantChatSystem: chatSystemPrompt,
			wantChatUser: "Context:\n" +
				"- cwd: /Users/x/project\n" +
				"- files: README.md src tests\n" +
				"- git: branch main (dirty)\n" +
				"- last command failed (exit 1)\n" +
				"- recent commands: git add .; git commit -m \"wip\"; git status\n" +
				"\n" +
				"Complete this command, keeping any needed leading space:\n" +
				"git com",
			wantMarkerPrefix: "# cwd: /Users/x/project\n" +
				"# files: README.md src tests\n" +
				"# git: branch main (dirty)\n" +
				"$ git add .\n" +
				"$ git commit -m \"wip\"\n" +
				"$ git status\n" +
				"# last command failed (exit 1)\n" +
				"$ git com",
			wantNoMarkerPrefix: "# cwd: /Users/x/project\n" +
				"# files: README.md src tests\n" +
				"# git: branch main (dirty)\n" +
				"git add .\n" +
				"git commit -m \"wip\"\n" +
				"git status\n" +
				"# last command failed (exit 1)\n" +
				"git com",
			wantExitAlwaysPfx: "# cwd: /Users/x/project\n" +
				"# files: README.md src tests\n" +
				"# git: branch main (dirty)\n" +
				"# last command failed (exit 1)\n" +
				"$ git add .\n" +
				"$ git commit -m \"wip\"\n" +
				"$ git status\n" +
				"# exit: 1\n" +
				"$ git com",
		},
		{
			name: "next-command/history-bearing",
			req: protocol.Request{
				V: protocol.Version, ID: "2", Kind: protocol.KindNextCommand,
				Buf: "", Cwd: "/Users/x/project", GitBranch: "main", GitDirty: false, LastExit: 0,
				History:    []string{"cd project", "ls", "git push"},
				DirEntries: []string{"go.mod", "main.go"},
			},
			wantChatSystem: chatSystemPrompt,
			wantChatUser: "Context:\n" +
				"- cwd: /Users/x/project\n" +
				"- files: go.mod main.go\n" +
				"- git: branch main\n" +
				"- recent commands: cd project; ls; git push\n" +
				"\n" +
				"The prompt is empty. Based on the recent commands and context above, predict the single most likely next command. Keep it short and common:\n",
			wantMarkerPrefix: "# cwd: /Users/x/project\n" +
				"# files: go.mod main.go\n" +
				"# git: branch main\n" +
				"$ cd project\n" +
				"$ ls\n" +
				"$ git push\n" +
				"$ ",
			wantNoMarkerPrefix: "# cwd: /Users/x/project\n" +
				"# files: go.mod main.go\n" +
				"# git: branch main\n" +
				"cd project\n" +
				"ls\n" +
				"git push\n",
			wantExitAlwaysPfx: "# cwd: /Users/x/project\n" +
				"# files: go.mod main.go\n" +
				"# git: branch main\n" +
				"$ cd project\n" +
				"$ ls\n" +
				"$ git push\n" +
				"# exit: 0\n" +
				"$ ",
		},
		{
			name: "typing/no-context",
			req: protocol.Request{
				V: protocol.Version, ID: "3", Kind: protocol.KindTyping, Buf: "doc",
			},
			wantChatSystem: chatSystemPrompt,
			wantChatUser: "Complete this command, keeping any needed leading space:\n" +
				"doc",
			wantMarkerPrefix:   "$ doc",
			wantNoMarkerPrefix: "doc",
			wantExitAlwaysPfx: "# exit: 0\n" +
				"$ doc",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chat := chatAppend.RenderChat(tc.req)
			if chat.System != tc.wantChatSystem {
				t.Errorf("chatAppend.RenderChat System mismatch:\ngot:  %q\nwant: %q", chat.System, tc.wantChatSystem)
			}
			if chat.User != tc.wantChatUser {
				t.Errorf("chatAppend.RenderChat User mismatch:\ngot:  %q\nwant: %q", chat.User, tc.wantChatUser)
			}

			marker := fimTranscriptMarker.RenderFIM(tc.req)
			if marker.Prefix != tc.wantMarkerPrefix {
				t.Errorf("fimTranscriptMarker.RenderFIM Prefix mismatch:\ngot:  %q\nwant: %q", marker.Prefix, tc.wantMarkerPrefix)
			}
			if marker.Suffix != "" {
				t.Errorf("fimTranscriptMarker.RenderFIM Suffix = %q, want \"\"", marker.Suffix)
			}

			noMarker := fimNoMarker.RenderFIM(tc.req)
			if noMarker.Prefix != tc.wantNoMarkerPrefix {
				t.Errorf("fimNoMarker.RenderFIM Prefix mismatch:\ngot:  %q\nwant: %q", noMarker.Prefix, tc.wantNoMarkerPrefix)
			}
			if noMarker.Suffix != "" {
				t.Errorf("fimNoMarker.RenderFIM Suffix = %q, want \"\"", noMarker.Suffix)
			}

			exitAlways := fimExitCodeAlways.RenderFIM(tc.req)
			if exitAlways.Prefix != tc.wantExitAlwaysPfx {
				t.Errorf("fimExitCodeAlways.RenderFIM Prefix mismatch:\ngot:  %q\nwant: %q", exitAlways.Prefix, tc.wantExitAlwaysPfx)
			}
			if exitAlways.Suffix != "" {
				t.Errorf("fimExitCodeAlways.RenderFIM Suffix = %q, want \"\"", exitAlways.Suffix)
			}
		})
	}
}

// TestFIMCommentedHistory checks fim-commented-history renders history as
// "# <cmd>" comment lines rather than raw transcript lines.
func TestFIMCommentedHistory(t *testing.T) {
	req := protocol.Request{
		V: protocol.Version, ID: "1", Kind: protocol.KindTyping,
		Buf: "git com", Cwd: "/Users/x/project", GitBranch: "main", GitDirty: true, LastExit: 1,
		History:    []string{"git add .", "git commit -m \"wip\"", "git status"},
		DirEntries: []string{"README.md", "src", "tests"},
	}
	want := "# cwd: /Users/x/project\n" +
		"# files: README.md src tests\n" +
		"# git: branch main (dirty)\n" +
		"# last command failed (exit 1)\n" +
		"# git add .\n" +
		"# git commit -m \"wip\"\n" +
		"# git status\n" +
		"$ git com"

	got := fimCommentedHistory.RenderFIM(req)
	if got.Prefix != want {
		t.Errorf("fimCommentedHistory.RenderFIM Prefix mismatch:\ngot:  %q\nwant: %q", got.Prefix, want)
	}
	if got.Suffix != "" {
		t.Errorf("fimCommentedHistory.RenderFIM Suffix = %q, want \"\"", got.Suffix)
	}
}

// TestFIMNoMarkerIsMarkerWithoutMarkers pins the relationship between the two
// prompts that used to share defaultFIMShape: fim-no-marker renders exactly
// what fim-transcript-marker renders with every "$ " marker stripped out.
func TestFIMNoMarkerIsMarkerWithoutMarkers(t *testing.T) {
	reqs := []protocol.Request{
		{
			Kind: protocol.KindTyping, Buf: "git com",
			Cwd: "/Users/x/project", GitBranch: "main", GitDirty: true, LastExit: 1,
			History:    []string{"git add .", "git commit -m \"wip\"", "git status"},
			DirEntries: []string{"README.md", "src", "tests"},
		},
		{
			Kind: protocol.KindNextCommand, Buf: "",
			Cwd: "/Users/x/project", GitBranch: "main",
			History:    []string{"cd project", "ls", "git push"},
			DirEntries: []string{"go.mod", "main.go"},
		},
		{Kind: protocol.KindTyping, Buf: "doc"},
	}
	for _, req := range reqs {
		marker := fimTranscriptMarker.RenderFIM(req).Prefix
		noMarker := fimNoMarker.RenderFIM(req).Prefix
		if want := strings.ReplaceAll(marker, "$ ", ""); noMarker != want {
			t.Errorf("fimNoMarker.RenderFIM(%+v) = %q, want %q (marker output with \"$ \" stripped)", req, noMarker, want)
		}
	}
}
