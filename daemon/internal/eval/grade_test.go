package eval

import (
	"context"
	"testing"

	"github.com/naasanov/zsh-autopilot/daemon/internal/protocol"
)

// gcase is one table-driven fixture for a Grader: an input Request, a
// candidate output suffix, and the expected presence.
type gcase struct {
	name string
	in   protocol.Request
	out  string
	want bool
}

func runGrader(t *testing.T, g Grader, cases []gcase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := g.Grade(context.Background(), c.in, c.out)
			if err != nil {
				t.Fatalf("Grade: unexpected error: %v", err)
			}
			if got != c.want {
				t.Errorf("Grade(%q) = %v, want %v", c.out, got, c.want)
			}
		})
	}
}

func TestMatchesRegexp(t *testing.T) {
	g := MatchesRegexp("test", `^\s*(&&|;)`)
	runGrader(t, g, []gcase{
		{"matches leading semicolon", protocol.Request{}, "; ls", true},
		{"matches leading &&", protocol.Request{}, "&& ls", true},
		{"no match", protocol.Request{}, " ls", false},
		{"empty", protocol.Request{}, "", false},
	})
}

func TestMatchesRegexp_PanicsOnBadPattern(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on malformed pattern")
		}
	}()
	MatchesRegexp("bad", "(unclosed")
}

func TestContains(t *testing.T) {
	g := Contains("push")
	runGrader(t, g, []gcase{
		{"exact", protocol.Request{}, "push", true},
		{"case-insensitive", protocol.Request{}, " PUSH origin", true},
		{"absent", protocol.Request{}, " commit", false},
		{"empty", protocol.Request{}, "", false},
	})
}

func TestContainsAny(t *testing.T) {
	g := ContainsAny("push", "commit")
	runGrader(t, g, []gcase{
		{"first", protocol.Request{}, " push", true},
		{"second", protocol.Request{}, " Commit -m", true},
		{"neither", protocol.Request{}, " status", false},
	})
}

func TestHasLeadingSpace(t *testing.T) {
	g := HasLeadingSpace()
	runGrader(t, g, []gcase{
		{"leading space", protocol.Request{}, " .", true},
		{"no space, mid-word", protocol.Request{}, "d", false},
		{"empty", protocol.Request{}, "", false},
		{"only space", protocol.Request{}, " ", true},
	})
}

func TestIsEmpty(t *testing.T) {
	g := IsEmpty()
	runGrader(t, g, []gcase{
		{"empty string", protocol.Request{}, "", true},
		{"whitespace only", protocol.Request{}, "   \t", true},
		{"non-empty", protocol.Request{}, " x", false},
		{"unicode whitespace-ish content", protocol.Request{}, " é", false},
	})
}

func TestLongerThan(t *testing.T) {
	g := LongerThan(8)
	runGrader(t, g, []gcase{
		{"empty", protocol.Request{}, "", false},
		{"exactly 8", protocol.Request{}, "12345678", false},
		{"9 runes", protocol.Request{}, "123456789", true},
		{"multibyte runes count as one", protocol.Request{}, "日本語日本語日本語日本語日本", true}, // >8 runes
	})
}

func TestContainsNewline(t *testing.T) {
	g := ContainsNewline()
	runGrader(t, g, []gcase{
		{"has newline", protocol.Request{}, "foo\nbar", true},
		{"has CR", protocol.Request{}, "foo\rbar", true},
		{"single line", protocol.Request{}, "foo bar", false},
		{"empty", protocol.Request{}, "", false},
	})
}

func TestContainsBacktickOrFence(t *testing.T) {
	g := ContainsBacktickOrFence()
	runGrader(t, g, []gcase{
		{"single backtick", protocol.Request{}, "run `ls`", true},
		{"triple fence", protocol.Request{}, "~~~\nls\n~~~", true},
		{"no fence", protocol.Request{}, " status", false},
	})
}

func TestLooksLikeProse(t *testing.T) {
	g := LooksLikeProse()
	runGrader(t, g, []gcase{
		{"sorry", protocol.Request{}, "Sorry, I can't do that", true},
		{"cannot", protocol.Request{}, "I cannot help with that", true},
		{"not enough", protocol.Request{}, "not enough context", true},
		{"unable", protocol.Request{}, "unable to determine", true},
		{"as an ai", protocol.Request{}, "As an AI, I cannot", true},
		{"here is", protocol.Request{}, "Here is the command", true},
		{"note colon", protocol.Request{}, "Note: this may fail", true},
		{"standalone I", protocol.Request{}, "I think status", true},
		{"does not match inside git", protocol.Request{}, "git status", false},
		{"does not match inside wait", protocol.Request{}, "please wait", false},
		{"plain completion", protocol.Request{}, " status", false},
		{"empty", protocol.Request{}, "", false},
	})
}

func TestRestatesBuffer(t *testing.T) {
	g := RestatesBuffer()
	buf := protocol.Request{Kind: protocol.KindTyping, Buf: "git status"}
	runGrader(t, g, []gcase{
		{"restates exactly", buf, " git status", true},
		{"restates with extra whitespace", buf, "  git   status ", true},
		{"restates then continues", buf, " git status --short", true},
		{"extends instead", buf, " -s", false},
		{"empty buffer never restates", protocol.Request{Buf: ""}, "", false},
		{"empty out with non-empty buf", buf, "", false},
	})
}

func TestNamesEntryInDirEntries(t *testing.T) {
	g := NamesEntryInDirEntries()
	in := protocol.Request{DirEntries: []string{"main.py", "utils.py", "README.md"}}
	runGrader(t, g, []gcase{
		{"names an entry", in, " main.py", true},
		{"names a quoted entry", in, ` "utils.py"`, true},
		{"names something absent", in, " missing.py", false},
		{"empty out", in, "", false},
		{"empty dir_entries", protocol.Request{}, " main.py", false},
	})
}

func TestNamesPathNotInDirEntries(t *testing.T) {
	g := NamesPathNotInDirEntries()
	in := protocol.Request{DirEntries: []string{"a.txt", "b.txt"}}
	runGrader(t, g, []gcase{
		{"names a listed file", in, " a.txt", false},
		{"names an unlisted file", in, " c.txt", true},
		{"no filename-shaped token", in, " -la", false},
		{"empty dir_entries never fires", protocol.Request{}, " nonexistent.txt", false},
		{"path-shaped and unlisted", in, " sub/dir.txt", true},
	})
}

func TestContainsTokenNotInContext(t *testing.T) {
	g := ContainsTokenNotInContext()
	in := protocol.Request{
		Buf:        "git switch -c ",
		GitBranch:  "main",
		History:    []string{"git status", "git log"},
		DirEntries: []string{"README.md"},
	}
	runGrader(t, g, []gcase{
		{"invented identifier fires", in, "my-invented-feature", true},
		{"flags do not fire", in, "-c", false},
		{"common command words do not fire", protocol.Request{}, "git switch -c", false},
		{"known-from-history token does not fire", in, "log", false},
		{"known-from-dir-entries token does not fire", in, "README.md", false},
		{"known-from-branch token does not fire", in, "main", false},
		{"pure numbers do not fire", protocol.Request{}, "42", false},
	})
}

func TestContainsURL(t *testing.T) {
	g := ContainsURL()
	runGrader(t, g, []gcase{
		{"https url", protocol.Request{}, " https://github.com/x/y.git", true},
		{"http url", protocol.Request{}, " http://example.com", true},
		{"scp-style", protocol.Request{}, " git@github.com:x/y.git", true},
		{"no url", protocol.Request{}, " origin", false},
		{"empty", protocol.Request{}, "", false},
	})
}

func TestContainsHostNotInHistory(t *testing.T) {
	g := ContainsHostNotInHistory()
	inKnown := protocol.Request{History: []string{"ssh user@myhost.example.com"}}
	inLocal := protocol.Request{History: []string{"curl http://localhost:3000/health"}}
	runGrader(t, g, []gcase{
		{"host in history", inKnown, " user@myhost.example.com", false},
		{"host absent from history", protocol.Request{}, " root@10.0.0.1", true},
		{"no hostname-shaped token", protocol.Request{}, " -v", false},
		{"empty history, dotted host fires", protocol.Request{}, " example.com", true},
		// The undotted-host-in-URL-position gap that made B7 a false pass.
		{"undotted host in url fires", protocol.Request{}, " -X GET http://localhost:8080/api/v1/books", true},
		{"undotted host known from history", inLocal, " http://localhost:3000/ready", false},
		{"same host, different port, still known", inLocal, " http://localhost:8080/ready", false},
		{"space-separated host and port is not host:port", protocol.Request{}, " nc db 5432", false},
		{"bare host:port outside url fires", protocol.Request{}, " nc redis:6379", true},
		// A clock time must not read as host 12 on port 30.
		{"pure-number token is not a host", protocol.Request{}, ` --since "12:30"`, false},
		{"header value is not a host", protocol.Request{}, ` -H "Content-Type: application/json"`, false},
	})
}

func TestClosesQuoteWithContent(t *testing.T) {
	g := ClosesQuoteWithContent()
	runGrader(t, g, []gcase{
		{"closes with content", protocol.Request{Buf: `git commit -m "`}, `wip changes"`, true},
		{"stops at open quote", protocol.Request{Buf: `git commit -m "`}, "", false},
		{"empty quotes only", protocol.Request{Buf: ""}, `""`, false},
		{"no quotes at all", protocol.Request{Buf: "git status"}, "", false},
	})
}

func TestEqualsRecentHistory(t *testing.T) {
	g := EqualsRecentHistory(4)
	in := protocol.Request{History: []string{"git status", "git diff", "git status", "git log"}}
	runGrader(t, g, []gcase{
		{"equals a recent entry", in, "git log", true},
		{"equals entry outside window", protocol.Request{History: []string{"old one", "git diff", "git status", "git log", "git add ."}}, "old one", false},
		{"does not equal anything recent", in, "git push", false},
		{"empty full command never matches", in, "", false},
	})
}

func TestInHistory(t *testing.T) {
	g := InHistory()
	in := protocol.Request{History: []string{"docker compose up -d", "docker ps"}}
	runGrader(t, g, []gcase{
		{"in history", in, "docker ps", true},
		{"not in history", in, "docker logs", false},
		{"empty", in, "", false},
	})
}

func TestEqualsHistoryModuloNumber(t *testing.T) {
	g := EqualsHistoryModuloNumber()
	in := protocol.Request{History: []string{"git tag v1.1.0", "git push --tags"}}
	runGrader(t, g, []gcase{
		{"digit changed fires", in, "git tag v1.2.0", true},
		{"exact repeat does not fire", in, "git tag v1.1.0", false},
		{"unrelated command does not fire", in, "git status", false},
		{"trailing digit changed fires", protocol.Request{History: []string{"mkdir test1", "cd test1"}}, "mkdir test2", true},
		{"empty out never fires", in, "", false},
	})
}

func TestContainsSeparator(t *testing.T) {
	g := ContainsSeparator()
	runGrader(t, g, []gcase{
		{"semicolon", protocol.Request{}, " proj; cd proj", true},
		{"double amp", protocol.Request{}, " proj && cd proj", true},
		{"no separator", protocol.Request{}, " proj", false},
	})
}

func TestStartsWithSeparator(t *testing.T) {
	g := StartsWithSeparator()
	runGrader(t, g, []gcase{
		{"leading semicolon", protocol.Request{}, "; source .venv/bin/activate", true},
		{"leading &&", protocol.Request{}, "&& ls", true},
		{"leading pipe", protocol.Request{}, "| less", true},
		{"leading double pipe", protocol.Request{}, "|| true", true},
		{"leading whitespace then separator", protocol.Request{}, "   ; ls", true},
		{"not leading", protocol.Request{}, "git status; ls", false},
		{"empty", protocol.Request{}, "", false},
	})
}

func TestContinuesLastHistoryCommand(t *testing.T) {
	g := ContinuesLastHistoryCommand()
	pushHistory := protocol.Request{History: []string{"git status", "git push"}}
	runGrader(t, g, []gcase{
		{"bare args continuing git push", pushHistory, "origin main", true},
		{"standalone new command", pushHistory, "git status", false},
		{"empty suggestion", pushHistory, "", false},
		{"no history", protocol.Request{}, "origin main", false},
		{"last entry isn't argumentless git subcommand", protocol.Request{History: []string{"git commit -m x"}}, "origin main", false},
		{"last entry isn't git", protocol.Request{History: []string{"npm push"}}, "origin main", false},
	})
}

func TestAnyOf(t *testing.T) {
	g := AnyOf("empty-or-short", IsEmpty(), func() Grader {
		return GraderFunc{N: "not-longer-than-8", F: func(_ protocol.Request, out string) (bool, error) {
			present, err := LongerThan(8).Grade(context.Background(), protocol.Request{}, out)
			return !present, err
		}}
	}())
	runGrader(t, g, []gcase{
		{"empty satisfies", protocol.Request{}, "", true},
		{"short satisfies", protocol.Request{}, "abc", true},
		{"long fails both", protocol.Request{}, "this is a long suggestion", false},
	})
}

func TestAllOf(t *testing.T) {
	g := AllOf("space-and-short", HasLeadingSpace(), GraderFunc{
		N: "short",
		F: func(_ protocol.Request, out string) (bool, error) {
			present, err := LongerThan(5).Grade(context.Background(), protocol.Request{}, out)
			return !present, err
		},
	})
	runGrader(t, g, []gcase{
		{"both true", protocol.Request{}, " abc", true},
		{"leading space but long", protocol.Request{}, " abcdefgh", false},
		{"short but no leading space", protocol.Request{}, "abc", false},
	})
}

func TestNot(t *testing.T) {
	g := Not(LongerThan(8))
	runGrader(t, g, []gcase{
		{"short is not-longer-than-8", protocol.Request{}, "abc", true},
		{"empty is not-longer-than-8", protocol.Request{}, "", true},
		{"long is not not-longer-than-8", protocol.Request{}, "123456789", false},
	})
}

func TestEchoesOtherCwdCommand(t *testing.T) {
	g := EchoesOtherCwdCommand()
	in := protocol.Request{Cwd: "/x/gotool"}
	in.SetHistory(
		protocol.HistoryIn("/x/gotool", "go mod tidy", "go build ./..."),
		protocol.HistoryIn("/x/webapp", "npm install", "npm run build", "npm test"),
	)
	runGrader(t, g, []gcase{
		{"first and second token both echo an other-cwd entry", in, "npm run build", true},
		{"first token echoes, no second token on the entry side", in, "npm", true},
		{"first token matches but second token diverges from every entry", in, "npm audit fix", false},
		{"matches the same-cwd entry, not other-cwd", in, "go build ./...", false},
		{"no match at all", in, "git status", false},
		{"empty out never fires", in, "", false},
	})

	noKnownOtherCwd := protocol.Request{Cwd: "/x/gotool"}
	noKnownOtherCwd.SetHistory(protocol.HistoryUnknown("npm install", "npm run build"))
	runGrader(t, g, []gcase{
		{"inert with only unknown-cwd entries", noKnownOtherCwd, "npm run build", false},
	})

	sameCwdOnly := protocol.Request{Cwd: "/x/gotool"}
	sameCwdOnly.SetHistory(protocol.HistoryIn("/x/gotool", "go mod tidy"))
	runGrader(t, g, []gcase{
		{"inert when every known cwd matches in.Cwd", sameCwdOnly, "go mod tidy", false},
	})

	runGrader(t, g, []gcase{
		{"inert on legacy case with no HistoryCwds at all", protocol.Request{Cwd: "/x/gotool", History: []string{"npm run build"}}, "npm run build", false},
	})
}

func TestOutputMentionsParentDirArtifact(t *testing.T) {
	g := OutputMentionsParentDirArtifact()
	in := protocol.Request{Cwd: "/home/dir"}
	runGrader(t, g, []gcase{
		{"mentions dotdot navigation", in, "cd ..", true},
		{"mentions the literal parent path", in, "ls /home", true},
		{"no parent-dir artifact", in, "echo 3", false},
		{"empty out", in, "", false},
		{"empty cwd never fires", protocol.Request{}, "cd ..", false},
		{"root cwd has no parent to leak", protocol.Request{Cwd: "/"}, "cd ..", false},
	})
}

func TestFullCommand(t *testing.T) {
	got := fullCommand(protocol.Request{Buf: "git add"}, " .")
	if got != "git add ." {
		t.Errorf("fullCommand = %q, want %q", got, "git add .")
	}
}
