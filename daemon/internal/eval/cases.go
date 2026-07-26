package eval

import "github.com/naasanov/zsh-autopilot/daemon/internal/protocol"

// This file is the deterministic case corpus (plan doc "Test cases" — Part
// 2's scope: categories A, B, D, F1, plus the deterministic E cases E1, E2,
// E4, E5, E6, E8, and the deterministic C cases C1/C2). The judged cases
// (C3, E3, E7, F2) are Part 3's business and are NOT here.
//
// Cases() is the single accessor cmd/eval drives; keep it in the plan doc's
// table order (A -> B -> C -> D -> E -> F) so a diff against the plan is a
// visual scan, not a search.

// Cases returns the full deterministic corpus, in stable ID order. Calling
// it repeatedly returns equivalent (fresh) slices/values — see
// cases_test.go's determinism check — since every Case is built fresh here
// rather than shared as package-level mutable state.
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
			// A5 guards the accumulator's first-line cutoff (accum.go). This
			// should almost NEVER fire in practice — the cutoff already
			// strips anything past the first newline before this grader
			// ever sees it — which is the point: it's a regression guard on
			// that cutoff, not a case that's expected to catch live
			// multi-line output. Do not delete it for "never firing"; a trip
			// here means the cutoff broke.
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
	}
}

// ---- B. Fabrication — "stop before free-form input you can't know" -------

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
			// model kept going past the point it should have stopped.
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
		{
			ID:       "B7",
			Category: "fabrication",
			Req:      protocol.Request{Kind: protocol.KindTyping, Buf: "curl "},
			Asserts: []Assertion{
				{Label: "invents-url-with-unknown-host", Polarity: MustNot, Threshold: 0.20,
					Grader: AllOf("url-with-unknown-host", ContainsURL(), ContainsHostNotInHistory())},
			},
		},
		{
			ID:       "B8",
			Category: "fabrication",
			Req:      protocol.Request{Kind: protocol.KindTyping, Buf: "ssh "},
			Asserts: []Assertion{
				{Label: "invents-hostname", Polarity: MustNot, Threshold: 0.20, Grader: ContainsHostNotInHistory()},
			},
		},
	}
}

// ---- C. Nonsense incrementing (deterministic cases only: C1/C2) ----------
//
// C3 (the general judged case) is Part 3's business.

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
			// this one. Any future suppressor must move D1/D3 down without
			// moving D2/D4 down (plan doc).
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

// ---- E. Context usage (deterministic cases only) --------------------------
//
// C3/E3/E7/F2's judged siblings are Part 3's business; the deterministic E
// cases here (E1, E2, E4, E5, E6, E8) are the reason the harness exists.

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
		{
			// E6 is the open-question (a) probe: does stale history from a
			// different project (8 npm commands) win over the CURRENT cwd
			// (a go module, no package.json in sight) once the transcript
			// has moved on ("cd ../gotool", "go mod tidy")? "npm" in the
			// output means the model anchored on stale history instead of
			// the live cwd/dir_entries.
			ID:       "E6",
			Category: "context",
			Req: protocol.Request{
				Kind: protocol.KindNextCommand,
				Cwd:  "/x/gotool",
				DirEntries: []string{
					"go.mod", "main.go",
				},
				History: []string{
					"npm install",
					"npm run build",
					"npm test",
					"npm run lint",
					"npm start",
					"npm run dev",
					"npm audit fix",
					"npm run deploy",
					"cd ../gotool",
					"go mod tidy",
				},
			},
			Asserts: []Assertion{
				{Label: "stale-history-wins", Polarity: MustNot, Threshold: 0.20, Grader: Contains("npm")},
			},
		},
		{
			// E8: with genuinely no context (no history, no cwd/git/dir
			// signal) there is nothing to predict from, so abstaining
			// (empty output) IS the correct behavior. This is the abstain
			// rate, tracked not asserted — there's no established target
			// yet, only a number to watch move.
			ID:       "E8",
			Category: "context",
			Req:      protocol.Request{Kind: protocol.KindNextCommand},
			Asserts: []Assertion{
				{Label: "abstains", Polarity: Measure, Grader: IsEmpty()},
			},
		},
	}
}

// ---- F. Abstention (deterministic case only: F1) --------------------------
//
// F2 (the judged "sensible continuation vs. noise" rubric) is Part 3's
// business.

func abstentionCases() []Case {
	return []Case{
		{
			ID:       "F1",
			Category: "abstention",
			Req:      protocol.Request{Kind: protocol.KindTyping, Buf: "asdkjhqwe"},
			Asserts: []Assertion{
				// Two assertions on one case, deliberately not one compound
				// grader (plan doc: "prefer multiple assertions... a
				// compound grader that fails tells you less"). AnyOf is
				// used ONLY for the genuine disjunction the plan describes
				// ("empty or <=8 chars").
				{Label: "empty-or-short", Polarity: Must, Threshold: 0.70,
					Grader: AnyOf("empty-or-not-longer-than-8", IsEmpty(), Not(LongerThan(8)))},
				{Label: "prose-markers", Polarity: TripWire, Grader: LooksLikeProse()},
			},
		},
	}
}
