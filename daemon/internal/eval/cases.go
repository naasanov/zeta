package eval

import "github.com/naasanov/zsh-autopilot/daemon/internal/protocol"

// This file is the full case corpus (plan doc "Test cases"): categories A,
// B, D, F1, the deterministic C/E cases (C1/C2, E1/E2/E4/E5/E6/E8), plus
// three judged cases (C3, E3, F2) graded through defaultJudgeGrader (judge.go).
//
// Cases() keeps the plan doc's table order (A -> B -> C -> D -> E -> F) so a
// diff against the plan is a visual scan. README.md in this directory
// indexes every case; update it alongside any case change.

// Cases returns the full corpus, in stable ID order. Every Case is built
// fresh on each call (no shared package-level state), so two calls produce
// equivalent values — see cases_test.go's determinism check, which compares
// judged-case Graders by Name() rather than identity.
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

// ---- A. Syntax — zero-tolerance trip-wires on shipped logic --------------

func syntaxCases() []Case {
	return []Case{
		{
			// A1 guards stripLeadingSeparators/firstShellCommand
			// (codestral.go): a next-command prediction must never lead with
			// a bare separator.
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
			// A2: the buffer already ends with a trailing space, so ANY
			// leading space in the completion produces a double space.
			ID:       "A2",
			Category: "syntax",
			Req:      protocol.Request{Kind: protocol.KindTyping, Buf: "git add "},
			Asserts: []Assertion{
				{Label: "double-space", Polarity: TripWire, Grader: HasLeadingSpace()},
			},
		},
		{
			// A3: the buffer has NO trailing space and the completion starts
			// a new word, so it needs to supply its own leading space. Not a
			// trip-wire — this is a quality bar on the spacing contract, not
			// a guard on code that makes the failure structurally
			// impossible, so it gets a measured threshold.
			ID:       "A3",
			Category: "syntax",
			Req:      protocol.Request{Kind: protocol.KindTyping, Buf: "git add"},
			Asserts: []Assertion{
				{Label: "supplies-leading-space", Polarity: Must, Threshold: 0.80, Grader: HasLeadingSpace()},
			},
		},
		{
			// A4 guards the same stripLeadingSeparators/firstShellCommand
			// logic as A1, from the over-chaining direction: a code model
			// chaining "mkdir proj; cd proj; git init" on one line.
			ID:       "A4",
			Category: "syntax",
			Req:      protocol.Request{Kind: protocol.KindTyping, Buf: "mkdir proj"},
			Asserts: []Assertion{
				{Label: "over-chains", Polarity: TripWire, Grader: ContainsSeparator()},
			},
		},
		{
			// A5 guards the accumulator's first-line cutoff (accum.go). It
			// should almost never fire — the cutoff already strips anything
			// past the first newline — so a trip here means the cutoff broke.
			ID:       "A5",
			Category: "syntax",
			Req:      protocol.Request{Kind: protocol.KindTyping, Buf: "grep -rn 'TODO' src"},
			Asserts: []Assertion{
				{Label: "multi-line-output", Polarity: TripWire, Grader: ContainsNewline()},
			},
		},
		{
			// A6: a bare word plus a directory listing is bait for a
			// chat-assistant reflex (wrapping the suggestion in a code
			// fence) that a raw shell completion must never do.
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
			// A7: "never repeat or restate a non-empty buffer" is a hard
			// rule in the system prompt (prompt.go) — mustNot rather than a
			// trip-wire only because it's plausible enough for a model to
			// slip on that a 10% ceiling is the honest bar, not zero.
			ID:       "A7",
			Category: "syntax",
			Req:      protocol.Request{Kind: protocol.KindTyping, Buf: "git status"},
			Asserts: []Assertion{
				{Label: "restates-buffer", Polarity: MustNot, Threshold: 0.10, Grader: RestatesBuffer()},
			},
		},
		{
			// A8: a nonsense token that isn't a real command; the model
			// must still emit a shell-shaped completion (or abstain), never
			// slip into chat-assistant prose.
			ID:       "A8",
			Category: "syntax",
			Req:      protocol.Request{Kind: protocol.KindTyping, Buf: "xyzzy "},
			Asserts: []Assertion{
				{Label: "prose-markers", Polarity: TripWire, Grader: LooksLikeProse()},
			},
		},
		{
			// A9 regression guard: history's last entry ("git push") is
			// already complete, but rendering it directly adjacent to the
			// predicted next command (no boundary marker) made the model
			// treat it as an unfinished buffer and append "origin main".
			// Fixed by RenderFIM's "$ " transcript marker (codestral.go);
			// `-variants fim-no-prompt-marker` re-runs this against the old
			// shape.
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

// ---- B. Fabrication — "stop before free-form input you can't know" -------

// A "b"-suffixed case pairs the bare case above it: same buffer, realistic
// context. Read each pair together — B1-B8 send almost no context, so a rate
// that collapses in the paired case means the fix is more grounded context,
// not a tighter output filter. The grounded value sits mid-history, never
// last, so a pass can't come from continuing the adjacent line.
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
			// B2: the ONLY acceptable output here is the opening quote and
			// its leading space (` "`, 2 runes) — anything longer means the
			// model kept going past the point it should have stopped. No
			// B1b/B2b: a commit message is free-form input no context supplies.
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
	}
}

// b3bCase: B3's own buffer has no paired form -- a branch being created
// does not exist yet, so no context can ground it. Switching to an
// EXISTING branch is the groundable sibling; every command ran in Cwd.
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

// b6bCase: ContainsURL (B6's grader) can't be reused here -- with a real
// remote in history, producing a URL is the CORRECT output. History moves
// across four directories before landing in Cwd.
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

// b7bCase: the known host is undotted (localhost:3000); every command ran
// in Cwd.
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

// b8bCase: the SSH host is reached twice mid-history, once via ssh and once
// via scp. cd ~/infra ran in the home directory; everything after ran in Cwd.
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

// ---- C. Nonsense incrementing ---------------------------------------------
//
// C1/C2 are narrow deterministic graders (normalize + diff, flag a
// numeric-only delta). C3 is the general case they can't catch — a
// mechanically-plausible next command that isn't actually useful — so it's
// judged instead.

// c3Rubric names the specific failure mode (mechanical version-bump) as the
// fail example rather than leaving "plausible" as a vague, judge-noising
// criterion.
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

// ---- D. Useless / looping — measured, not asserted -----------------------

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
			// D2 is the "correct repeat" control: after a failing build, a
			// suppressor that punishes ALL repeats would wrongly suppress
			// this one.
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
				// "suggestion == git status" is exactly "equals the last
				// history entry" here, so EqualsRecentHistory(1) (the last
				// 1 entry) expresses it without a bespoke equality grader.
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
	}
}

// ---- E. Context usage -------------------------------------------------------
//
// E1/E2/E4/E5/E6/E8 are deterministic; E3 is judged -- "did it react to the
// failure" requires judging intent, not matching a substring. E10/E11/E14
// judge directory-following and fit for the daemon-owned-history cases.

// e3Rubric: a failed build is the most common signal a next-command
// prediction should react to; the fail example keeps the judge from
// crediting an unrelated-but-plausible-looking suggestion.
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

// e10Rubric: E9's judged sibling -- Contains("npm") can't see "invented
// something unrelated to either project", which needs a judge to catch.
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

// e11Rubric: history is deliberately from a DIFFERENT project (Node/npm)
// than the live cwd/dir_entries (Rust) -- probes whether the model still
// grounds its suggestion in the current directory with zero same-dir history.
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

// e14Rubric probes whether the suggestion still fits /home/dir despite an
// interleaved transcript that can make the parent dir look adjacent to
// the cursor.
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
				// Narrower than NamesEntryInDirEntries: the plan pins this
				// to the two runnable scripts, not "any dir entry" (which
				// would also accept README.md — a real dir entry, but not a
				// sensible python target).
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
			// E8: with no context at all, abstaining (empty output) is
			// correct. Tracked, not asserted — no established target yet.
			ID:       "E8",
			Category: "context",
			Req:      protocol.Request{Kind: protocol.KindNextCommand},
			Asserts: []Assertion{
				{Label: "abstains", Polarity: Measure, Grader: IsEmpty()},
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

// e6Case: does stale history (8 npm commands) win over the CURRENT cwd (a
// Go module) once the transcript has moved on? The same-dir go signal sits
// directly adjacent to the cursor -- E9 pushes it far away instead.
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

// e9Case pushes the same-dir go signal far from the cursor, behind five
// webapp/npm commands, using per-entry cwds -- E6 carries the same signal
// adjacent to the cursor instead.
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

// e10Case: E9's judged sibling, same Request -- a substring/token check
// can't see "the model invented something unrelated to either project",
// which needs a judge (e10Rubric).
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

// e11Case: zero same-dir history anywhere in the pool -- does a cwd-aware
// prompt's empty history block buy correctness, or just silence? The judged
// assertion checks directory-following directly; IsEmpty is Measure, so an
// abstention-driven pass reads as a number, not a pass on its own.
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

// e12Case: the counter-hypothesis case. Right after a "cd", every relevant
// history entry is tagged with the PREVIOUS directory -- hard cwd filtering
// should HURT here, dropping the only useful context there is.
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

// e13Case: the actual shipping shape -- bootstrapped ("" cwd) entries
// followed by tagged ones. The only case that exercises the "" policy
// end to end.
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

// e14Case interleaves /home/dir with a visit to its parent /home: filtering
// to /home/dir drops the intermediate hop, so the transcript can read as
// continuous when it wasn't. Measures the leak instead of asserting on it.
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
//
// F1 covers plain garbage input. F2 is judged: a buffer that's already a
// complete, valid command, where the only correct behaviors are "append a
// sensible continuation" or "abstain" — never noise.

// f2Rubric states explicitly that an empty suggestion is a GOOD outcome, so
// the judge doesn't penalize a correct abstention for having nothing to
// evaluate.
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
				// Two assertions, not one compound grader — a compound
				// grader that fails tells you less. AnyOf here is a genuine
				// disjunction ("empty or <=8 chars").
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
