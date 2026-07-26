package eval

import "github.com/naasanov/zsh-autopilot/daemon/internal/protocol"

// This file is the full case corpus (plan doc "Test cases"): categories A,
// B, D, F1, the deterministic E cases (E1, E2, E4, E5, E6, E8), the
// deterministic C cases (C1/C2) from Part 2, plus Part 3's four judged
// cases (C3, E3, E7, F2), each backed by a rubric graded through
// defaultJudgeGrader (judge.go) rather than a deterministic Grader.
//
// Cases() is the single accessor cmd/eval drives; keep it in the plan doc's
// table order (A -> B -> C -> D -> E -> F) so a diff against the plan is a
// visual scan, not a search.

// Cases returns the full corpus, in stable ID order. Calling it repeatedly
// returns equivalent (fresh) slices/values — see cases_test.go's determinism
// check — since every Case is built fresh here rather than shared as
// package-level mutable state. This holds for the judged cases too:
// defaultJudgeGrader re-resolves the judge from the environment on every
// call rather than caching a package-level Judge, so two Cases() calls in
// the same process/environment produce Graders with equal Name() (the only
// thing TestCases_Deterministic compares a Grader on).
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

// ---- C. Nonsense incrementing ---------------------------------------------
//
// C1/C2 are Part 2's narrow deterministic graders (normalize + diff, flag a
// numeric-only delta). C3 is the general case they can't catch: a
// mechanically-plausible-looking next command that isn't actually what a
// developer would do next, which needs a judge's judgment rather than a
// regex.

// c3Rubric is deliberately concrete: "plausible" alone is exactly the vague
// criterion the plan doc warns produces a noisy judge, so it names the
// specific failure mode (mechanical version-bump for no reason) as the fail
// example.
const c3Rubric = `The context is a shell history ending in a git tag-and-push sequence. You
are shown a suggested next command. Answer: is this suggestion a plausible
next command a developer would actually run in this situation (e.g.
verifying the release, checking CI, opening a PR, bumping a changelog),
rather than mechanical pattern-continuation with no real justification (e.g.
bumping the version number again to tag ANOTHER release immediately, or
repeating the same tag/push with a trivial numeric change)? Pass if it is a
plausible, purposeful next step; fail if it is mechanical continuation of
the version-bump pattern for its own sake. You are looking only at this
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

// ---- E. Context usage -------------------------------------------------------
//
// E1, E2, E4, E5, E6, E8 (Part 2, deterministic) are the reason the harness
// exists. E3 and E7 (Part 3, judged) cover context-usage questions no regex
// can answer: "did it react to the failure" and "did it follow the CURRENT
// directory over stale history" both require judging the suggestion's
// intent, not just matching a substring.

// e3Rubric: a failed build is the single most common signal a next-command
// prediction should react to. The fail example is explicit so the judge
// doesn't credit an unrelated-but-superficially-plausible suggestion.
const e3Rubric = `The context shows the last command in history was "go build ./..." and it
exited with a non-zero status (a failed build). Answer: does the suggested
next command respond to that failure — e.g. retrying the build, running a
narrower build/vet/test to see the error, inspecting the error output
(cat/less/grep on a log), or editing the file that likely failed? Fail if
the suggestion ignores the failure entirely and moves on to unrelated work
(e.g. a plain "git status", "ls", or starting a new, unrelated task) as if
the previous command had succeeded. You are looking only at this question,
not at syntax or formatting.`

// e7Rubric: the history is deliberately from a DIFFERENT project (Node,
// npm/yarn) than the live cwd/dir_entries/git_branch (a Go project) — this
// is the open-question (a) probe from the plan doc ("FIM prompt shape"):
// does top-placed cwd/git make the model assume all history ran in the
// current directory? The rubric asks the judge to grade exactly that
// tension, not whether the suggestion is "good" in general.
const e7Rubric = `The shell history shown is from a DIFFERENT project than the current
directory: the history lines are Node.js/npm commands, but the current
working directory, directory listing, and git branch all describe a Go
project (go.mod, .go files) with no package.json in sight. Answer: does the
suggested next command follow the CURRENT directory/project (e.g. a
go/git/shell command appropriate to a Go project) rather than continuing
the STALE npm/yarn history as if it still applied? Fail if the suggestion
is an npm/yarn/node command, or otherwise assumes the history's project
context still holds. You are looking only at this question, not at syntax
or formatting.`

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
			// E7 is E6's judged sibling, same open-question (a) probe, but
			// asking the general question directly instead of pattern-
			// matching for "npm": does the suggestion follow the CURRENT
			// directory over stale cross-project history.
			ID:       "E7",
			Category: "context",
			Req: protocol.Request{
				Kind:      protocol.KindNextCommand,
				Cwd:       "/x/gotool",
				GitBranch: "main",
				DirEntries: []string{
					"go.mod", "main.go",
				},
				History: []string{
					"npm install",
					"npm run build",
					"npm test",
					"npm run lint",
					"npm start",
				},
			},
			Asserts: []Assertion{
				{Label: "follows-current-directory", Polarity: Must, Threshold: 0.60,
					Grader: defaultJudgeGrader("E7", "follows-current-directory", e7Rubric)},
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

// ---- F. Abstention ----------------------------------------------------------
//
// F1 (Part 2, deterministic) covers plain garbage input. F2 (Part 3, judged)
// covers the harder case a regex can't grade: a buffer that's already a
// complete, valid command, where the ONLY correct behaviors are "append a
// sensible continuation" or "abstain" — never noise.

// f2Rubric explicitly states that an empty suggestion is a GOOD outcome
// here, per the task brief: without that line a judge grading "is the
// appended text a sensible continuation" naively would penalize a correct
// abstention for having no continuation to evaluate.
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
