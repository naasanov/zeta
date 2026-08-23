# Eval case corpus

Human-readable index of every case in [`cases.go`](cases.go). The harness
design (sampling, grading mechanics, judge selection) lives in
`.docs/eval_harness_plan.md`; this file just documents *what each case is
checking and why*, so you don't have to read the Go to know what a red cell
in the scorecard means.

One row per **assertion** - most cases have one, a few (A9 aside, see F1)
have more than one, each catching a different failure mode on the same
input. The "Assertion" column folds polarity + threshold + grader into one
sentence:

- **Must X% of the time** - presence of the shape is desired; X is a floor.
- **Must not X% of the time** - presence is undesired; X is a ceiling.
- **Trip-wire** - presence is a defect; a single occurrence anywhere in the
  sample fails the case, no threshold.
- **Tracked only (Measure)** - no pass/fail; the harness just reports the
  rate so you have a number to watch move.
- **(judge)** - graded by an LLM judge against a written rubric instead of a
  deterministic string/regex check.

## A. Syntax - zero-tolerance trip-wires on shipped logic

| Case | Tag | Short name | Description | Assertion |
|---|---|---|---|---|
| A1 | `leads-with-separator` | No leading separator on next-command | Guards `stripLeadingSeparators`/`firstShellCommand` (codestral.go): a next-command prediction must never lead with a bare separator like `; ` or `&&`. | Trip-wire - must never lead with a separator (`StartsWithSeparator`). |
| A2 | `double-space` | No double space when buffer already ends in a space | Buffer is `"git add "` (trailing space); any leading space in the completion produces a visible double space. | Trip-wire - must never add a leading space (`HasLeadingSpace`). |
| A3 | `supplies-leading-space` | Supplies its own leading space when needed | Buffer is `"git add"` (no trailing space) and the completion starts a new word - a quality bar on the spacing contract, not a structurally-guaranteed case, hence `must`/threshold rather than a trip-wire. | Must supply a leading space (`HasLeadingSpace`) at least 80% of the time. |
| A4 | `over-chains` | No chaining multiple commands on one line | Same underlying logic as A1, from the over-chaining direction: a code model stringing `mkdir proj; cd proj; git init` onto one line. | Trip-wire - must never contain a command separator (`ContainsSeparator`). |
| A5 | `multi-line-output` | Multi-line output never survives the accumulator | Regression guard on the accumulator's first-line cutoff (accum.go). Should almost never fire in practice - the cutoff strips anything past the first newline before this grader sees it. A trip here means the cutoff broke; do not delete for "never firing." | Trip-wire - must never contain a newline (`ContainsNewline`). |
| A6 | `wraps-in-fence` | No markdown code-fence wrapping | A bare word plus a directory listing is bait for a chat-assistant reflex - wrapping the suggestion in a code fence/backticks - that a raw shell completion must never do. | Trip-wire - must never contain a backtick or code fence (`ContainsBacktickOrFence`). |
| A7 | `restates-buffer` | Doesn't restate the existing buffer | "Never repeat or restate a non-empty buffer" is a hard system-prompt rule; `mustNot` rather than trip-wire because it's plausible enough for a model to slip on that a 10% ceiling is the honest bar, not zero. | Must not restate the buffer (`RestatesBuffer`) more than 10% of the time. |
| A8 | `prose-markers` | No chat-assistant prose on a nonsense token | Input isn't a real command; the model must still emit a shell-shaped completion (or abstain), never slip into chat-assistant explanation. | Trip-wire - must never contain prose markers (`LooksLikeProse`). |
| A9 | `continues-last-history-command` | Doesn't treat a finished history line as an unfinished buffer | Real dogfooding bug, now **fixed** - this is its regression guard. History's last entry (`git push`) is already complete, but `RenderFIM` used to render history and the predicted next command on adjacent lines with nothing but a newline between them, so the model appended `origin main` instead of predicting a standalone next command. Fixed by the `$ ` transcript marker now in `RenderFIM`'s shipped shape; `-variants fim-no-prompt-marker` re-runs this against the old shape. | Trip-wire - must never continue the last history line as if it were an unfinished buffer (`ContinuesLastHistoryCommand`). |

## B. Fabrication - stop before free-form input you can't know

A **`b`-suffixed case pairs the bare case above it**: same buffer, realistic
context. B1-B8 send almost no context, which is the model's worst regime and not
the common one - a real shell sends up to 30 history entries. **Read each pair
together.** A rate that collapses in the paired case means the fix is to widen
grounded context; a rate that holds means the fix has to constrain the output.
In every pair the grounded value sits mid-history, never last, so a pass can't
come from continuing the adjacent line.

The `uses-known-*` legs are `Measure`, not `Must` - "did it fabricate" is the
calibrated question here, and a second uncalibrated threshold would only add a
red cell that means nothing until Part 5.

`-cases B6` selects the bare case alone; `-cases B6*` takes the pair.

B1/B2 have no pair by design: a commit message is free-form input no amount of
context can supply, and copying a previous message verbatim is its own defect -
"stop at the quote" stays the only right answer.

| Case | Tag | Short name | Description | Assertion |
|---|---|---|---|---|
| B1 | `invents-and-closes-message` | Doesn't fabricate a commit message | Buffer is an open `git commit -m "` quote with dirty git state; the model can't know the developer's intended message and must not invent a plausible-looking one and close the quote. | Must not close the quote with fabricated content (`ClosesQuoteWithContent`) more than 10% of the time. |
| B2 | `continues-past-open-quote` | Stops right after the open quote | Buffer is `git commit -m` with no trailing quote yet. The only acceptable output is the opening quote plus its leading space (2 runes) - anything longer means the model kept going past where it should have stopped. | Must not produce output longer than 2 characters (`LongerThan(2)`) more than 20% of the time. |
| B3 | `invents-branch-name` | Doesn't invent a branch name | Buffer is `git switch -c ` with no branch name anywhere in context or history. | Must not contain a token absent from context/history (`ContainsTokenNotInContext`) more than 20% of the time. |
| B3b | `invents-branch-name` | Doesn't invent a branch when switching to an existing one | B3's own buffer has no high-context form - a branch being created does not exist yet, so no context can ground it. Switching to an **existing** branch is the groundable sibling: buffer is `git switch ` with both `main` and `feature/auth-refactor` in history and the latter as the current branch. | Must not contain a token absent from context (`ContainsTokenNotInContext`) more than 20% of the time. Measures whether a known branch was named. |
| B4 | `names-real-file` | Names a real file from the directory listing | Buffer is `cat ` with dir entries available; correct behavior is naming an actual entry, not fabricating one. | Must name an entry present in `DirEntries` (`NamesEntryInDirEntries`) at least 80% of the time. |
| B5 | `names-fabricated-file` | Doesn't fabricate a filename for `rm` | Buffer is `rm ` with a small known dir listing. Destructive command, so fabricating a target is the highest-stakes version of this failure mode. | Must not name a path absent from `DirEntries` (`NamesPathNotInDirEntries`) more than 10% of the time. |
| B6 | `invents-remote-url` | Doesn't invent a git remote URL | Buffer is `git remote add origin ` with no URL anywhere in context. | Must not contain a URL (`ContainsURL`) more than 20% of the time. |
| B6b | `invents-remote-host` | Doesn't invent a remote URL when a real one is in reach | Buffer is `git remote add origin ` with a `git clone https://github.com/naasanov/dotfiles.git` mid-history and a `mkdir zeta`/`git init` sequence after it. B6's `ContainsURL` grader is deliberately NOT reused - here a URL is the correct output, so fabrication has to be measured as an unfamiliar host or an invented owner/repo. Both graders had a false-positive bug on this exact case (fixed 20260823): the `.git` leaf of a correctly-grounded URL (`.../zeta.git`) was reading as an unrelated second host, and the URL was tokenized as one atomic string so a known host+org with a correctly-*inferred* leaf never matched. | Must not name a host absent from history (`ContainsHostNotInHistory`) more than 20% of the time, and must not use a token absent from context (`ContainsTokenNotInContext`) more than 30% of the time. Measures whether `github.com/naasanov` was used. |
| B7 | `invents-url-with-unknown-host` | Doesn't invent a URL to an unfamiliar host | Buffer is `curl ` with no host in history. Compound grader: checks both that a URL was produced AND that its host wasn't seen before. | Must not produce a URL whose host is absent from history (`ContainsURL` AND `ContainsHostNotInHistory`) more than 20% of the time. |
| B7b | `invents-url-with-unknown-host` | Doesn't invent a URL when a real endpoint is in history | Buffer is `curl ` with two `curl http://localhost:3000/...` calls mid-history. The known host is **undotted** - exactly the shape `ContainsHostNotInHistory` used to miss, which made B7 a false pass. | Must not name a host absent from history (`ContainsHostNotInHistory`) more than 20% of the time. Measures whether `localhost:3000` was used. |
| B8 | `invents-hostname` | Doesn't invent an SSH hostname | Buffer is `ssh ` with no host anywhere in history. | Must not name a host absent from history (`ContainsHostNotInHistory`) more than 20% of the time. |
| B8b | `invents-hostname` | Doesn't invent an SSH host when one was reached twice | Buffer is `ssh ` with `deploy@build01.internal.example.com` appearing twice mid-history (once via `ssh`, once via `scp`). | Must not name a host absent from history (`ContainsHostNotInHistory`) more than 20% of the time. Measures whether the known host was used. |
| B9 | `names-branch-from-other-repo` | Doesn't suggest a branch that only exists in a DIFFERENT repo's history | cwd is `/x/aws-services` on branch `VAPI-3675`; history has a real branch (`billing-prod-VAPI-3673`) tagged to a different repo's cwd (`/x/billing-service`). `ContainsTokenNotInContext` can't catch this - it flattens `History` across all cwds, so the foreign branch reads as "known" vocabulary. `ContainsOtherCwdOnlyToken` scopes the known-token check to `in.Cwd` instead. | Must not name a token seen only under a different cwd (`ContainsOtherCwdOnlyToken`) more than 20% of the time. |

## C. Nonsense incrementing

C1/C2 are narrow deterministic pattern checks (Part 2). C3 and C1b are the
general case they can't catch - a mechanically-plausible-*looking* next
command that isn't actually what a developer would do next - which needs a
judge's judgment rather than a regex.

| Case | Tag | Short name | Description | Assertion |
|---|---|---|---|---|
| C1 | `mechanical-version-bump` | Doesn't mechanically re-bump a version tag | History ends in a tag-and-push sequence (`git tag v1.1.0`, `git push --tags`); flags "same command, only the number changed" continuations. | Must not equal the last history entry modulo a numeric change (`EqualsHistoryModuloNumber`) more than 20% of the time. |
| C1b | `plausible-next-command` | C1's own judged sibling | Same Request as C1. `EqualsHistoryModuloNumber` only fires on a byte-identical shape after digit normalization, so a mechanical bump wrapped in extra flags/args (e.g. `git tag -a v1.1.1 -m "version 1.1.1"`) is still the same bad pattern but slips past it untouched. Added after a manual review (20260823) found every sampled completion doing exactly this while C1 read a clean PASS. | Must be judged plausible and purposeful (LLM judge, `c1bRubric`) at least 70% of the time. |
| C2 | `mechanical-digit-bump` | Doesn't mechanically bump a directory-name digit | History is `mkdir test1; cd test1`; same pattern-continuation check applied to an incrementing digit in a directory name instead of a version tag. | Must not equal the last history entry modulo a numeric change (`EqualsHistoryModuloNumber`) more than 20% of the time. |
| C3 | `plausible-next-command` | Suggests a purposeful next step, not mechanical continuation | History ends in a tag-and-push sequence (`v0.1.6`). Judged: is the suggestion a plausible, purposeful next step (verify release, check CI, open a PR, bump a changelog) rather than mechanical pattern-continuation (e.g. bumping the version again for no reason)? An empty suggestion PASSES - abstaining is correct when only a mechanical continuation is available. | Must be judged plausible and purposeful (LLM judge) at least 70% of the time. |

## D. Useless / looping

D1/D3/D4 measure without asserting - suppression risks eating a
legitimately-repeated command, so these track the rate rather than fail on
it. D5/D6 assert `MustNot` on the two narrower shapes where a repeat has no
possible justification; D2 stays the "correct repeat" control either way.

| Case | Tag | Short name | Description | Assertion |
|---|---|---|---|---|
| D1 | `equals-recent-command` | Rate of repeating a recent history entry (4-window) | History alternates `git status`/`git diff`/`git status`/`git log`; tracks whether the model loops back to something recently issued instead of proposing something new. | Tracked only (Measure) - reports how often the suggestion equals one of the last 4 history entries (`EqualsRecentHistory(4)`). |
| D2 | `retries-build-or-test` | Retries build/test after a failure - the "correct repeat" control | After a failing `go build ./...`, repeating IS correct behavior. Guards against a future repeat-suppressor being added carelessly: any such suppressor must move D1/D3 down without moving D2/D4 down. | Must suggest `go build` or `go test` (`ContainsAny`) at least 80% of the time. |
| D3 | `equals-git-status` | Rate of repeating `git status` verbatim | History ends `git add .; git status` with dirty git; tracks whether the model just repeats the last command instead of proposing the next logical step (e.g. commit). Equivalent to `EqualsRecentHistory(1)`, so no bespoke equality grader was written. | Tracked only (Measure) - reports how often the suggestion equals the last 1 history entry (`EqualsRecentHistory(1)`). |
| D4 | `in-history` | Rate of repeating ANY prior history entry | History is `docker compose up -d; docker ps`. Broader looping signal than D1/D3 - checks membership across the full history, not just a fixed recent window. | Tracked only (Measure) - reports how often the suggestion matches any entry in history (`InHistory`). |
| D5 | `repeats-last-command` | Doesn't repeat a just-finished one-off command with no justification | History ends `vim main.go; go install .../goimports@latest` - no failure, no dirty state, nothing calling for a repeat. Asserts the shape D1/D3 only measure, using the same `EqualsRecentHistory(1)` grader. | Must not equal the last history entry (`EqualsRecentHistory(1)`) more than 20% of the time. |
| D6 | `switches-to-current-branch` | Doesn't suggest switching to the branch you're already on | `GitBranch` is `main`; history includes an earlier `git switch main`. Suggesting that switch again is a no-op. | Must not suggest a switch/checkout targeting `main` (`AllOf(ContainsAny("switch","checkout"), Contains("main"))`) more than 20% of the time. |

## E. Context usage

E1/E2/E4/E5/E6/E8 are deterministic (Part 2, the original reason the harness
exists). E3 (Part 3) is judged: "did it react to the failure" requires
judging intent, not matching a substring. The "did it follow the CURRENT
directory over stale history" question is judged directly on E10/E11/E14.

E9-E14 (the daemon-owned-history feature) probe cwd-scoped retrieval and the
new `cwd-filtered`/`cwd-grouped` prompts, using per-entry cwd tags
(`protocol.HistoryIn`/`HistoryUnknown`/`SetHistory`). E6 also carries
accurate per-entry tags now (adjacent to the cursor), so it activates the
same cwd-aware prompts as E9-E14, not just baseline ones. E9/E13 restate
E6's "same-dir signal sits far from the cursor" geometry with the signal now
pushed away instead of adjacent; E11/E12 probe the two failure modes of hard
filtering (buying correctness vs. just buying silence, and hurting right
after a `cd` when all relevant history is in the previous dir); E14 probes a
filtering artifact - interleaved directories can make a filtered transcript
look more contiguous than it was.

E11's `follows-current-directory` judged assertion has no hand labels yet in
`testdata/judge_labels.jsonl` (those are scoped to C3/E3/F2), so it can't be
scored by `-judge-validate` until it gets its own labels.

| Case | Tag | Short name | Description | Assertion |
|---|---|---|---|---|
| E1 | `suggests-commit` | Suggests committing after staging dirty changes | Git is dirty, last command was `git add .`; the obvious next step is a commit. | Must contain "commit" (`Contains`) at least 80% of the time. |
| E2 | `suggests-push` | Suggests pushing after a clean commit | Git is clean after a commit; the obvious next step is a push. | Must contain "push" (`Contains`) at least 70% of the time. |
| E3 | `responds-to-failed-build` | Reacts to a failed build instead of ignoring it | Last command `go build ./...` exited non-zero. Judged: does the suggestion respond to the failure (retry, narrower build/vet/test, inspect error output, edit the file) rather than moving on to unrelated work as if it succeeded? An empty suggestion FAILS - a failed build has an obvious correct response. | Must be judged as responding to the failure (LLM judge) at least 70% of the time. |
| E4 | `names-a-runnable-script` | Names a runnable Python script, not just any dir entry | Buffer is `python ` with dir entries including a non-script `README.md`. Narrower than the general fabrication check (B4-style) - pins the suggestion to one of the two runnable scripts specifically. | Must match exactly `main.py` or `utils.py` (`MatchesRegexp`) at least 80% of the time. |
| E5 | `names-current-branch` | Names the current git branch when pushing | Buffer is `git push origin ` on branch `feature/auth`; correct completion is the live branch name from context. | Must contain "feature/auth" (`Contains`) at least 80% of the time. |
| E6 | `stale-history-wins` | Doesn't let stale npm history override the current Go directory | Open-question (a) probe from the design doc's "FIM prompt shape" section: does top-placed cwd/git make the model assume ALL history ran in the current directory? History is 8 npm commands tagged `/x/webapp`, then `cd ../gotool` and `go mod tidy` tagged `/x/gotool` - the same-dir signal sits directly adjacent to the cursor (E9 pushes it far away instead). Deterministic pattern-match for "npm" leaking into the suggestion. | Must not contain "npm" (`Contains`) more than 20% of the time. |
| E8 | `abstains` | Abstention rate with genuinely no context | No history, no cwd/git/dir signal - there's nothing to predict from, so abstaining (empty output) IS correct. Was `Measure`-only ("no established target yet"); promoted to `Must` after a manual review (20260823) found every sampled completion hallucinating instead of abstaining, with the case reading a permanent clean PASS regardless. | Must be empty (`IsEmpty`) at least 70% of the time. |
| E9 | `stale-history-wins`, `echoes-other-cwd-command` | Same-dir signal exists but sits far from the cursor | cwd is `/x/gotool`; history is 2 Go commands tagged `/x/gotool` followed by 5 npm commands tagged `/x/webapp` - E6's geometry with the same-dir signal pushed away from the cursor instead of adjacent to it. `EchoesOtherCwdCommand` generalizes `Contains("npm")`: it fires on any suggestion that parrots a command tagged with a different, known cwd, not just this one project's vocabulary. | Must not contain "npm" (`Contains`) more than 20% of the time, and must not echo an other-cwd command (`EchoesOtherCwdCommand`) more than 20% of the time. |
| E10 | `follows-current-directory` | E9's judged sibling | Same Request as E9. Judged because `Contains("npm")` can't see "invented something unrelated to either project" - the failure mode a pattern match misses. | Must be judged as following the current Go directory and giving a purposeful suggestion (LLM judge, `e10Rubric`) at least 60% of the time. |
| E11 | `stale-history-wins`, `abstains`, `follows-current-directory` | Zero same-dir history anywhere in the pool | cwd is `/x/newproj` (dir entries `Cargo.toml`, `src`); all history is npm commands tagged `/x/webapp` - no same-dir signal exists at all. Probes whether a cwd-aware prompt's empty history block buys correctness or just silence; the judged leg folds in the former "follows the current directory" question, asked directly instead of pattern-matching for "npm". | Must not contain "npm" (`Contains`) more than 20% of the time. Tracked only (Measure) - also reports the abstention rate (`IsEmpty`). Must be judged as following the current directory (LLM judge, `e11Rubric`) at least 60% of the time. |
| E12 | `uses-prior-dir-context`, `abstains` | The counter-hypothesis: right after a `cd`, hard filtering should HURT | cwd is `/x/dotfiles`; `ls`, `git clone ...dotfiles`, `cd dotfiles` are all tagged `/x` (the previous directory) - zero same-dir signal, but the previous-dir history is exactly what's relevant right after a `cd`. | Must contain one of "ls"/"install"/"cat" (`ContainsAny`) at least 60% of the time. Tracked only (Measure) - also reports the abstention rate (`IsEmpty`). |
| E13 | `stale-history-wins`, `echoes-other-cwd-command` | The actual shipping shape: bootstrapped then tagged | cwd is `/x/gotool`; 3 npm commands carry `""` cwd (bootstrapped from `$HISTFILE` before the daemon started tagging), followed by 2 Go commands tagged `/x/gotool`. The only case exercising the `""`-cwd policy end to end. | Must not contain "npm" (`Contains`) more than 20% of the time, and must not echo an other-cwd command (`EchoesOtherCwdCommand`) more than 20% of the time. |
| E14 | `mentions-parent-dir-artifact`, `fits-current-directory` | Interleaving artifact from a cwd filter | cwd is `/home/dir`; history runs `echo 1`/`cd ..` in `/home/dir`, then `echo 2`/`cd dir` in `/home`, then `echo 3` back in `/home/dir`. Filtering to `/home/dir` drops the intermediate `/home` hop, so the filtered transcript reads as continuous when it wasn't - no cd-filtering is applied here; the leak is measured instead of asserted on. | Tracked only (Measure) - reports how often the output mentions the parent directory (`OutputMentionsParentDirArtifact`). Must be judged as fitting the current directory (LLM judge, `e14Rubric`) at least 60% of the time. |

## F. Abstention

F1 (deterministic) covers plain garbage input. F2 (judged) covers the harder
case a regex can't grade: a buffer that's already a complete, valid command,
where the only correct behaviors are "append a sensible continuation" or
"abstain" - never noise.

| Case | Tag | Short name | Description | Assertion |
|---|---|---|---|---|
| F1 | `empty-or-short` | Abstains or keeps it very short on plain garbage input | Buffer is nonsense (`asdkjhqwe`). Deliberately two separate assertions on this case rather than one compound grader - a compound grader that fails tells you less. | Must be empty or no longer than 8 characters (`AnyOf(IsEmpty, Not(LongerThan(8)))`) at least 70% of the time. |
| F1 | `prose-markers` | No chat-assistant prose on garbage input | Same case, second assertion - guards against the model responding to nonsense with an explanation instead of shell-shaped output or silence. | Trip-wire - must never contain prose markers (`LooksLikeProse`). |
| F2 | `sensible-continuation-or-abstain` | Sensible continuation or correct abstention on an already-complete command | Buffer `ls -la` is already complete and valid. Judged: the only correct behaviors are appending a sensible continuation (pipe/redirect) or abstaining; the rubric explicitly credits an empty suggestion as a GOOD outcome, not a missed one. | Must be judged sensible-or-abstaining (LLM judge) at least 70% of the time. |

## Adding a case

New cases go in `cases.go`, grouped by category, in the plan doc's A → B → C
→ D → E → F table order so a diff against the plan is a visual scan. Add a
row here in the matching section - keep the Description focused on the
*failure mode being guarded against*, not a restatement of the Req/Assert
fields (those are already in the code).
