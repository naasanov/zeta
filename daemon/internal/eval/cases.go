package eval

import "github.com/naasanov/zsh-autopilot/daemon/internal/protocol"

// Cases() keeps the plan doc's table order (A -> B -> C -> D -> E -> F).
// README.md in this directory indexes every case; update it alongside any
// case change.

// Cases returns the full corpus, in stable ID order. Every Case is built
// fresh on each call, so two calls produce equivalent values.
func Cases() []Case {
	var cases []Case
	cases = append(cases, syntaxCases()...)
	cases = append(cases, fabricationCases()...)
	cases = append(cases, incrementingCases()...)
	cases = append(cases, loopingCases()...)
	cases = append(cases, contextCases()...)
	cases = append(cases, abstentionCases()...)
	return cases
}

// ---- A. Syntax - zero-tolerance trip-wires on shipped logic --------------

func syntaxCases() []Case {
	return []Case{
		{
			ID:       "A1",
			Category: "syntax",
			Req: protocol.Request{
				Kind:    protocol.KindNextCommand,
				History: []string{"git add .", `git commit -m "wip"`},
			},
			Asserts: []Assertion{
				{Label: "leads-with-separator", Polarity: TripWire, Grader: StartsWithSeparator()},
			},
		},
		{
			ID:       "A2",
			Category: "syntax",
			Req:      protocol.Request{Kind: protocol.KindTyping, Buf: "git add "},
			Asserts: []Assertion{
				{Label: "double-space", Polarity: TripWire, Grader: HasLeadingSpace()},
			},
		},
		{
			ID:       "A3",
			Category: "syntax",
			Req:      protocol.Request{Kind: protocol.KindTyping, Buf: "git add"},
			Asserts: []Assertion{
				{Label: "supplies-leading-space", Polarity: Must, Threshold: 0.80, Grader: HasLeadingSpace()},
			},
		},
		{
			ID:       "A4",
			Category: "syntax",
			Req:      protocol.Request{Kind: protocol.KindTyping, Buf: "mkdir proj"},
			Asserts: []Assertion{
				{Label: "over-chains", Polarity: TripWire, Grader: ContainsSeparator()},
			},
		},
		{
			ID:       "A5",
			Category: "syntax",
			Req:      protocol.Request{Kind: protocol.KindTyping, Buf: "grep -rn 'TODO' src"},
			Asserts: []Assertion{
				{Label: "multi-line-output", Polarity: TripWire, Grader: ContainsNewline()},
			},
		},
		{
			ID:       "A6",
			Category: "syntax",
			Req: protocol.Request{
				Kind:       protocol.KindTyping,
				Buf:        "doc",
				DirEntries: []string{"README.md", "docs"},
			},
			Asserts: []Assertion{
				{Label: "wraps-in-fence", Polarity: TripWire, Grader: ContainsBacktickOrFence()},
			},
		},
		{
			ID:       "A7",
			Category: "syntax",
			Req:      protocol.Request{Kind: protocol.KindTyping, Buf: "git status"},
			Asserts: []Assertion{
				{Label: "restates-buffer", Polarity: MustNot, Threshold: 0.10, Grader: RestatesBuffer()},
			},
		},
		{
			ID:       "A8",
			Category: "syntax",
			Req:      protocol.Request{Kind: protocol.KindTyping, Buf: "xyzzy "},
			Asserts: []Assertion{
				{Label: "prose-markers", Polarity: TripWire, Grader: LooksLikeProse()},
			},
		},
		{
			ID:       "A9",
			Category: "syntax",
			Req: protocol.Request{
				Kind:    protocol.KindNextCommand,
				History: []string{"git status", "git push"},
			},
			Asserts: []Assertion{
				{Label: "continues-last-history-command", Polarity: TripWire, Grader: ContinuesLastHistoryCommand()},
			},
		},
	}
}

// ---- B. Fabrication - "stop before free-form input you can't know" -------

func fabricationCases() []Case {
	return []Case{
		{
			ID:       "B1",
			Category: "fabrication",
			Req: protocol.Request{
				Kind:     protocol.KindTyping,
				Buf:      `git commit -m "`,
				GitDirty: true,
			},
			Asserts: []Assertion{
				{Label: "invents-and-closes-message", Polarity: MustNot, Threshold: 0.10, Grader: ClosesQuoteWithContent()},
			},
		},
		{
			ID:       "B2",
			Category: "fabrication",
			Req: protocol.Request{
				Kind:     protocol.KindTyping,
				Buf:      `git commit -m`,
				GitDirty: true,
			},
			Asserts: []Assertion{
				{Label: "continues-past-open-quote", Polarity: MustNot, Threshold: 0.20, Grader: LongerThan(2)},
			},
		},
		{
			ID:       "B3",
			Category: "fabrication",
			Req: protocol.Request{
				Kind:      protocol.KindTyping,
				Buf:       "git switch -c ",
				GitBranch: "main",
				History:   []string{"git status", "git log", "git pull"},
			},
			Asserts: []Assertion{
				{Label: "invents-branch-name", Polarity: MustNot, Threshold: 0.20, Grader: ContainsTokenNotInContext()},
			},
		},
		b3bCase(),
		{
			ID:       "B4",
			Category: "fabrication",
			Req: protocol.Request{
				Kind:       protocol.KindTyping,
				Buf:        "cat ",
				DirEntries: []string{"README.md", "main.go", "go.mod"},
			},
			Asserts: []Assertion{
				{Label: "names-real-file", Polarity: Must, Threshold: 0.80, Grader: NamesEntryInDirEntries()},
			},
		},
		{
			ID:       "B5",
			Category: "fabrication",
			Req: protocol.Request{
				Kind:       protocol.KindTyping,
				Buf:        "rm ",
				DirEntries: []string{"a.txt", "b.txt"},
			},
			Asserts: []Assertion{
				{Label: "names-fabricated-file", Polarity: MustNot, Threshold: 0.10, Grader: NamesPathNotInDirEntries()},
			},
		},
		{
			ID:       "B6",
			Category: "fabrication",
			Req:      protocol.Request{Kind: protocol.KindTyping, Buf: "git remote add origin "},
			Asserts: []Assertion{
				{Label: "invents-remote-url", Polarity: MustNot, Threshold: 0.20, Grader: ContainsURL()},
			},
		},
		b6bCase(),
		{
			ID:       "B7",
			Category: "fabrication",
			Req:      protocol.Request{Kind: protocol.KindTyping, Buf: "curl "},
			Asserts: []Assertion{
				{Label: "invents-url-with-unknown-host", Polarity: MustNot, Threshold: 0.20,
					Grader: AllOf("url-with-unknown-host", ContainsURL(), ContainsHostNotInHistory())},
			},
		},
		b7bCase(),
		{
			ID:       "B8",
			Category: "fabrication",
			Req:      protocol.Request{Kind: protocol.KindTyping, Buf: "ssh "},
			Asserts: []Assertion{
				{Label: "invents-hostname", Polarity: MustNot, Threshold: 0.20, Grader: ContainsHostNotInHistory()},
			},
		},
		b8bCase(),
		b9Case(),
	}
}

func b3bCase() Case {
	req := protocol.Request{
		Kind:      protocol.KindTyping,
		Buf:       "git switch ",
		Cwd:       "/Users/dev/projects/zeta",
		GitBranch: "feature/auth-refactor",
	}
	req.SetHistory(
		protocol.HistoryIn("/Users/dev/projects/zeta",
			"git switch -c feature/auth-refactor",
			"git add .",
			`git commit -m "wip"`,
			"git push -u origin feature/auth-refactor",
			"git switch main",
			"git pull",
			"git switch feature/auth-refactor",
			"go test ./...",
		),
	)
	return Case{
		ID:       "B3b",
		Category: "fabrication",
		Req:      req,
		Asserts: []Assertion{
			{Label: "invents-branch-name", Polarity: MustNot, Threshold: 0.20, Grader: ContainsTokenNotInContext()},
			{Label: "uses-known-branch", Polarity: Measure, Grader: ContainsAny("feature/auth-refactor", "main")},
		},
	}
}

func b6bCase() Case {
	req := protocol.Request{
		Kind: protocol.KindTyping,
		Buf:  "git remote add origin ",
		Cwd:  "/Users/dev/projects/zeta",
	}
	req.SetHistory(
		protocol.HistoryIn("/Users/dev", "cd ~/projects"),
		protocol.HistoryIn("/Users/dev/projects", "git clone https://github.com/naasanov/dotfiles.git", "cd dotfiles"),
		protocol.HistoryIn("/Users/dev/projects/dotfiles", "git log --oneline", "cd .."),
		protocol.HistoryIn("/Users/dev/projects", "mkdir zeta", "cd zeta"),
		protocol.HistoryIn("/Users/dev/projects/zeta", "git init", "git add .", `git commit -m "initial commit"`),
	)
	return Case{
		ID:       "B6b",
		Category: "fabrication",
		Req:      req,
		Asserts: []Assertion{
			{Label: "invents-remote-host", Polarity: MustNot, Threshold: 0.20, Grader: ContainsHostNotInHistory()},
			{Label: "invents-token-not-in-context", Polarity: MustNot, Threshold: 0.30, Grader: ContainsTokenNotInContext()},
			{Label: "uses-known-host", Polarity: Measure, Grader: Contains("github.com/naasanov")},
		},
	}
}

func b7bCase() Case {
	req := protocol.Request{
		Kind: protocol.KindTyping,
		Buf:  "curl ",
		Cwd:  "/Users/dev/projects/api",
	}
	req.SetHistory(
		protocol.HistoryIn("/Users/dev/projects/api",
			"npm install",
			"npm run dev",
			"curl http://localhost:3000/health",
			"git status",
			"npm test",
			"curl http://localhost:3000/api/users",
			"docker compose up -d",
			"git add .",
		),
	)
	return Case{
		ID:       "B7b",
		Category: "fabrication",
		Req:      req,
		Asserts: []Assertion{
			{Label: "invents-url-with-unknown-host", Polarity: MustNot, Threshold: 0.20, Grader: ContainsHostNotInHistory()},
			{Label: "uses-known-host", Polarity: Measure, Grader: Contains("localhost:3000")},
		},
	}
}

func b8bCase() Case {
	req := protocol.Request{
		Kind: protocol.KindTyping,
		Buf:  "ssh ",
		Cwd:  "/Users/dev/infra",
	}
	req.SetHistory(
		protocol.HistoryIn("/Users/dev", "cd ~/infra"),
		protocol.HistoryIn("/Users/dev/infra",
			"ssh deploy@build01.internal.example.com",
			"terraform plan",
			"git status",
			"scp ./app.conf deploy@build01.internal.example.com:/etc/app/",
			"terraform apply",
			"git add .",
			`git commit -m "bump instance size"`,
		),
	)
	return Case{
		ID:       "B8b",
		Category: "fabrication",
		Req:      req,
		Asserts: []Assertion{
			{Label: "invents-hostname", Polarity: MustNot, Threshold: 0.20, Grader: ContainsHostNotInHistory()},
			{Label: "uses-known-host", Polarity: Measure, Grader: Contains("build01.internal.example.com")},
		},
	}
}

func b9Case() Case {
	req := protocol.Request{
		Kind:      protocol.KindNextCommand,
		Cwd:       "/Users/dev/projects/infra",
		GitBranch: "infra-fix",
	}
	req.SetHistory(
		protocol.HistoryIn("/Users/dev/projects/billing-service",
			"git switch -c billing-fix",
			"git add .",
			`git commit -m "billing 400 fix"`,
			"git push",
		),
		protocol.HistoryIn("/Users/dev/projects/infra",
			"git switch infra-fix",
			"terraform plan",
			"git add .",
			`git commit -m "billing config"`,
			"git push",
		),
	)
	return Case{
		ID:       "B9",
		Category: "fabrication",
		Req:      req,
		Asserts: []Assertion{
			{Label: "names-branch-from-other-repo", Polarity: MustNot, Threshold: 0.20, Grader: ContainsOtherCwdOnlyToken()},
		},
	}
}

// ---- C. Nonsense incrementing ---------------------------------------------

const c3Rubric = `The context is a shell history ending in a git tag-and-push sequence. You
are shown a suggested next command. Answer: is this suggestion a plausible
next command a developer would actually run in this situation (e.g.
verifying the release, checking CI, opening a PR, bumping a changelog),
rather than mechanical pattern-continuation with no real justification (e.g.
bumping the version number again to tag ANOTHER release immediately, or
repeating the same tag/push with a trivial numeric change)? Pass if it is a
plausible, purposeful next step; fail if it is mechanical continuation of
the version-bump pattern for its own sake. An EMPTY suggestion PASSES: with
only a mechanical version bump available as a pattern-continuation, abstaining
is the correct call, not a missed one. You are looking only at this
question, not at syntax or formatting.`

const c1bRubric = `The context is a shell history ending in a git tag-and-push sequence. You
are shown a suggested next command. Answer: is this suggestion a plausible
next command a developer would actually run in this situation (e.g.
verifying the release, checking CI, opening a PR, bumping a changelog),
rather than mechanical pattern-continuation with no real justification (e.g.
bumping the version number again to tag ANOTHER release immediately, or
repeating the same tag/push with a trivial numeric change — even if wrapped
in extra flags or a commit-style message)? Pass if it is a plausible,
purposeful next step; fail if it is mechanical continuation of the
version-bump pattern for its own sake. An EMPTY suggestion PASSES: with only
a mechanical version bump available as a pattern-continuation, abstaining is
the correct call, not a missed one. You are looking only at this question,
not at syntax or formatting.`

func incrementingCases() []Case {
	return []Case{
		{
			ID:       "C1",
			Category: "incrementing",
			Req: protocol.Request{
				Kind:    protocol.KindNextCommand,
				History: []string{"git tag v1.1.0", "git push --tags"},
			},
			Asserts: []Assertion{
				{Label: "mechanical-version-bump", Polarity: MustNot, Threshold: 0.20, Grader: EqualsHistoryModuloNumber()},
			},
		},
		{
			ID:       "C1b",
			Category: "incrementing",
			Req: protocol.Request{
				Kind:    protocol.KindNextCommand,
				History: []string{"git tag v1.1.0", "git push --tags"},
			},
			Asserts: []Assertion{
				{Label: "plausible-next-command", Polarity: Must, Threshold: 0.70,
					Grader: defaultJudgeGrader("C1b", "plausible-next-command", c1bRubric)},
			},
		},
		{
			ID:       "C2",
			Category: "incrementing",
			Req: protocol.Request{
				Kind:    protocol.KindNextCommand,
				History: []string{"mkdir test1", "cd test1"},
			},
			Asserts: []Assertion{
				{Label: "mechanical-digit-bump", Polarity: MustNot, Threshold: 0.20, Grader: EqualsHistoryModuloNumber()},
			},
		},
		{
			ID:       "C3",
			Category: "incrementing",
			Req: protocol.Request{
				Kind:    protocol.KindNextCommand,
				History: []string{"git tag v0.1.6", "git push origin v0.1.6"},
			},
			Asserts: []Assertion{
				{Label: "plausible-next-command", Polarity: Must, Threshold: 0.70,
					Grader: defaultJudgeGrader("C3", "plausible-next-command", c3Rubric)},
			},
		},
	}
}

// ---- D. Useless / looping --------------------------------------------------

func loopingCases() []Case {
	return []Case{
		{
			ID:       "D1",
			Category: "looping",
			Req: protocol.Request{
				Kind:    protocol.KindNextCommand,
				History: []string{"git status", "git diff", "git status", "git log"},
			},
			Asserts: []Assertion{
				{Label: "equals-recent-command", Polarity: Measure, Grader: EqualsRecentHistory(4)},
			},
		},
		{
			ID:       "D2",
			Category: "looping",
			Req: protocol.Request{
				Kind:     protocol.KindNextCommand,
				History:  []string{"vim main.go", "go build ./..."},
				LastExit: 1,
			},
			Asserts: []Assertion{
				{Label: "retries-build-or-test", Polarity: Must, Threshold: 0.80, Grader: ContainsAny("go build", "go test")},
			},
		},
		{
			ID:       "D3",
			Category: "looping",
			Req: protocol.Request{
				Kind:     protocol.KindNextCommand,
				History:  []string{"git add .", "git status"},
				GitDirty: true,
			},
			Asserts: []Assertion{
				{Label: "equals-git-status", Polarity: Measure, Grader: EqualsRecentHistory(1)},
			},
		},
		{
			ID:       "D4",
			Category: "looping",
			Req: protocol.Request{
				Kind:    protocol.KindNextCommand,
				History: []string{"docker compose up -d", "docker ps"},
			},
			Asserts: []Assertion{
				{Label: "in-history", Polarity: Measure, Grader: InHistory()},
			},
		},
		{
			ID:       "D5",
			Category: "looping",
			Req: protocol.Request{
				Kind:    protocol.KindNextCommand,
				History: []string{"vim main.go", "go install golang.org/x/tools/cmd/goimports@latest"},
			},
			Asserts: []Assertion{
				{Label: "repeats-last-command", Polarity: MustNot, Threshold: 0.20, Grader: EqualsRecentHistory(1)},
			},
		},
		{
			ID:       "D6",
			Category: "looping",
			Req: protocol.Request{
				Kind:      protocol.KindNextCommand,
				GitBranch: "main",
				History:   []string{"git switch main", "git pull", "npm install"},
			},
			Asserts: []Assertion{
				{Label: "switches-to-current-branch", Polarity: MustNot, Threshold: 0.20,
					Grader: AllOf("switch-or-checkout-main", ContainsAny("switch", "checkout"), Contains("main"))},
			},
		},
	}
}

// ---- E. Context usage -------------------------------------------------------

const e3Rubric = `The context shows the last command in history was "go build ./..." and it
exited with a non-zero status (a failed build). Answer: does the suggested
next command respond to that failure — e.g. retrying the build, running a
narrower build/vet/test to see the error, inspecting the error output
(cat/less/grep on a log), or editing the file that likely failed? Fail if
the suggestion ignores the failure entirely and moves on to unrelated work
(e.g. a plain "git status", "ls", or starting a new, unrelated task) as if
the previous command had succeeded. An EMPTY suggestion FAILS: a failed
build has an obvious correct response (retry, inspect, fix), so producing
nothing is a miss, not a judgement call. You are looking only at this
question, not at syntax or formatting.`

const e10Rubric = `The shell history shown starts with two Go commands (go mod tidy, go
build ./...) run in the current directory, followed by five npm commands
from a DIFFERENT project (/x/webapp) that sit closer to the cursor. The
current working directory is the Go project. Answer: does the suggested
next command follow the CURRENT Go directory (e.g. a go/git/shell command
appropriate to a Go project) rather than continuing the closer-but-stale
npm history, and is it a real, sensible next step rather than an invented
command unrelated to either project? Fail if the suggestion is an
npm/yarn/node command, or is unrelated to both projects. An EMPTY
suggestion FAILS: a Go-appropriate command is available from the earlier
history, so silence here is a miss, not a judgement call. You are looking
only at this question, not at syntax or formatting.`

const e11Rubric = `The shell history shown is from a DIFFERENT project than the current
directory: the history lines are Node.js/npm commands, but the current
working directory contains Cargo.toml and src — a Rust project, with no
package.json in sight. Answer: does the suggested next command follow the
CURRENT directory/project (e.g. a cargo/git/shell command appropriate to a
Rust project) rather than continuing the STALE npm/yarn history as if it
still applied? Fail if the suggestion is an npm/yarn/node command, or
otherwise assumes the history's project context still holds. An EMPTY suggestion FAILS: a Rust-appropriate command is clearly available from the
current directory listing, so silence here is a miss. You are looking only
at this question, not at syntax or formatting.`

const e14Rubric = `The current working directory is /home/dir. The shell history available
includes commands run in /home/dir and, interleaved with them, a visit to
its parent directory /home (a "cd .." followed later by a "cd dir" back).
Answer: does the suggested next command make sense as something run in
/home/dir right now, rather than something that assumes the session is
still in the parent directory /home or confuses the two? An EMPTY
suggestion FAILS: with recent same-directory history available, silence is
a miss, not a safe default. You are looking only at this question, not at
syntax or formatting.`

func contextCases() []Case {
	return []Case{
		{
			ID:       "E1",
			Category: "context",
			Req: protocol.Request{
				Kind:      protocol.KindNextCommand,
				GitBranch: "main",
				GitDirty:  true,
				History:   []string{"git add ."},
			},
			Asserts: []Assertion{
				{Label: "suggests-commit", Polarity: Must, Threshold: 0.80, Grader: Contains("commit")},
			},
		},
		{
			ID:       "E2",
			Category: "context",
			Req: protocol.Request{
				Kind:      protocol.KindNextCommand,
				GitBranch: "main",
				GitDirty:  false,
				History:   []string{`git commit -m "x"`},
			},
			Asserts: []Assertion{
				{Label: "suggests-push", Polarity: Must, Threshold: 0.70, Grader: Contains("push")},
			},
		},
		{
			ID:       "E3",
			Category: "context",
			Req: protocol.Request{
				Kind:     protocol.KindNextCommand,
				LastExit: 1,
				History:  []string{"vim main.go", "go build ./..."},
			},
			Asserts: []Assertion{
				{Label: "responds-to-failed-build", Polarity: Must, Threshold: 0.70,
					Grader: defaultJudgeGrader("E3", "responds-to-failed-build", e3Rubric)},
			},
		},
		{
			ID:       "E4",
			Category: "context",
			Req: protocol.Request{
				Kind:       protocol.KindTyping,
				Buf:        "python ",
				DirEntries: []string{"main.py", "utils.py", "README.md"},
			},
			Asserts: []Assertion{
				{Label: "names-a-runnable-script", Polarity: Must, Threshold: 0.80,
					Grader: MatchesRegexp("suffix-is-main-or-utils", `^\s*"?(main\.py|utils\.py)"?\s*$`)},
			},
		},
		{
			ID:       "E5",
			Category: "context",
			Req: protocol.Request{
				Kind:      protocol.KindTyping,
				Buf:       "git push origin ",
				GitBranch: "feature/auth",
			},
			Asserts: []Assertion{
				{Label: "names-current-branch", Polarity: Must, Threshold: 0.80, Grader: Contains("feature/auth")},
			},
		},
		e6Case(),
		{
			ID:       "E8",
			Category: "context",
			Req:      protocol.Request{Kind: protocol.KindNextCommand},
			Asserts: []Assertion{
				{Label: "abstains", Polarity: Must, Threshold: 0.70, Grader: IsEmpty()},
			},
		},
		e9Case(),
		e10Case(),
		e11Case(),
		e12Case(),
		e13Case(),
		e14Case(),
	}
}

func e6Case() Case {
	req := protocol.Request{
		Kind:       protocol.KindNextCommand,
		Cwd:        "/x/gotool",
		DirEntries: []string{"go.mod", "main.go"},
	}
	req.SetHistory(
		protocol.HistoryIn("/x/webapp", "npm install", "npm run build", "npm test", "npm run lint",
			"npm start", "npm run dev", "npm audit fix", "npm run deploy", "cd ../gotool"),
		protocol.HistoryIn("/x/gotool", "go mod tidy"),
	)
	return Case{
		ID:       "E6",
		Category: "context",
		Req:      req,
		Asserts: []Assertion{
			{Label: "stale-history-wins", Polarity: MustNot, Threshold: 0.20, Grader: Contains("npm")},
		},
	}
}

func e9Case() Case {
	req := protocol.Request{Kind: protocol.KindNextCommand, Cwd: "/x/gotool"}
	req.SetHistory(
		protocol.HistoryIn("/x/gotool", "go mod tidy", "go build ./..."),
		protocol.HistoryIn("/x/webapp", "npm install", "npm run build", "npm test", "npm run lint", "npm start"),
	)
	return Case{
		ID:       "E9",
		Category: "context",
		Req:      req,
		Asserts: []Assertion{
			{Label: "stale-history-wins", Polarity: MustNot, Threshold: 0.20, Grader: Contains("npm")},
			{Label: "echoes-other-cwd-command", Polarity: MustNot, Threshold: 0.20, Grader: EchoesOtherCwdCommand()},
		},
	}
}

func e10Case() Case {
	req := protocol.Request{Kind: protocol.KindNextCommand, Cwd: "/x/gotool"}
	req.SetHistory(
		protocol.HistoryIn("/x/gotool", "go mod tidy", "go build ./..."),
		protocol.HistoryIn("/x/webapp", "npm install", "npm run build", "npm test", "npm run lint", "npm start"),
	)
	return Case{
		ID:       "E10",
		Category: "context",
		Req:      req,
		Asserts: []Assertion{
			{Label: "follows-current-directory", Polarity: Must, Threshold: 0.60,
				Grader: defaultJudgeGrader("E10", "follows-current-directory", e10Rubric)},
		},
	}
}

func e11Case() Case {
	req := protocol.Request{
		Kind:       protocol.KindNextCommand,
		Cwd:        "/x/newproj",
		DirEntries: []string{"Cargo.toml", "src"},
	}
	req.SetHistory(
		protocol.HistoryIn("/x/webapp", "npm install", "npm run build", "npm test", "npm run lint", "npm start"),
	)
	return Case{
		ID:       "E11",
		Category: "context",
		Req:      req,
		Asserts: []Assertion{
			{Label: "stale-history-wins", Polarity: MustNot, Threshold: 0.20, Grader: Contains("npm")},
			{Label: "abstains", Polarity: Measure, Grader: IsEmpty()},
			{Label: "follows-current-directory", Polarity: Must, Threshold: 0.60,
				Grader: defaultJudgeGrader("E11", "follows-current-directory", e11Rubric)},
		},
	}
}

func e12Case() Case {
	req := protocol.Request{Kind: protocol.KindNextCommand, Cwd: "/x/dotfiles"}
	req.SetHistory(
		protocol.HistoryIn("/x", "ls", "git clone https://github.com/naasanov/dotfiles.git", "cd dotfiles"),
	)
	return Case{
		ID:       "E12",
		Category: "context",
		Req:      req,
		Asserts: []Assertion{
			{Label: "uses-prior-dir-context", Polarity: Must, Threshold: 0.60, Grader: ContainsAny("ls", "install", "cat")},
			{Label: "abstains", Polarity: Measure, Grader: IsEmpty()},
		},
	}
}

func e13Case() Case {
	req := protocol.Request{Kind: protocol.KindNextCommand, Cwd: "/x/gotool"}
	req.SetHistory(
		protocol.HistoryUnknown("npm install", "npm run build", "npm test"),
		protocol.HistoryIn("/x/gotool", "go mod tidy", "go build ./..."),
	)
	return Case{
		ID:       "E13",
		Category: "context",
		Req:      req,
		Asserts: []Assertion{
			{Label: "stale-history-wins", Polarity: MustNot, Threshold: 0.20, Grader: Contains("npm")},
			{Label: "echoes-other-cwd-command", Polarity: MustNot, Threshold: 0.20, Grader: EchoesOtherCwdCommand()},
		},
	}
}

func e14Case() Case {
	req := protocol.Request{Kind: protocol.KindNextCommand, Cwd: "/home/dir"}
	req.SetHistory(
		protocol.HistoryIn("/home/dir", "echo 1", "cd .."),
		protocol.HistoryIn("/home", "echo 2", "cd dir"),
		protocol.HistoryIn("/home/dir", "echo 3"),
	)
	return Case{
		ID:       "E14",
		Category: "context",
		Req:      req,
		Asserts: []Assertion{
			{Label: "mentions-parent-dir-artifact", Polarity: Measure, Grader: OutputMentionsParentDirArtifact()},
			{Label: "fits-current-directory", Polarity: Must, Threshold: 0.60,
				Grader: defaultJudgeGrader("E14", "fits-current-directory", e14Rubric)},
		},
	}
}

// ---- F. Abstention ----------------------------------------------------------

const f2Rubric = `The buffer already typed is "ls -la" — a complete, valid, self-contained
shell command with common flags. You are shown what (if anything) the
system suggests appending after it. An EMPTY suggestion is a GOOD, correct
outcome here: there is nothing useful to add after a command this complete,
so abstaining is not a failure. Answer: is the appended text (if any) either
(a) empty/whitespace-only (a correct abstention), or (b) a sensible,
plausible continuation a developer might actually type after "ls -la" (e.g.
piping to grep/less/wc, redirecting output)? Fail only if the appended text
is noise: nonsense, restates/repeats the buffer, prose/explanation, or an
unrelated fabricated command chained on. You are looking only at this
question, not at other syntax or formatting concerns.`

func abstentionCases() []Case {
	return []Case{
		{
			ID:       "F1",
			Category: "abstention",
			Req:      protocol.Request{Kind: protocol.KindTyping, Buf: "asdkjhqwe"},
			Asserts: []Assertion{
				{Label: "empty-or-short", Polarity: Must, Threshold: 0.70,
					Grader: AnyOf("empty-or-not-longer-than-8", IsEmpty(), Not(LongerThan(8)))},
				{Label: "prose-markers", Polarity: TripWire, Grader: LooksLikeProse()},
			},
		},
		{
			ID:       "F2",
			Category: "abstention",
			Req:      protocol.Request{Kind: protocol.KindTyping, Buf: "ls -la"},
			Asserts: []Assertion{
				{Label: "sensible-continuation-or-abstain", Polarity: Must, Threshold: 0.70,
					Grader: defaultJudgeGrader("F2", "sensible-continuation-or-abstain", f2Rubric)},
			},
		},
	}
}
