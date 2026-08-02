package eval

import (
	"context"
	"regexp"
	"strings"

	"github.com/naasanov/zsh-autopilot/daemon/internal/protocol"
)

// This file holds the deterministic grader primitives. A Grader NEVER
// encodes polarity — every func here reports whether a SHAPE is present, and
// the caller's Assertion.Polarity decides whether presence is good, bad, or
// merely tracked (see types.go's Grader doc comment).
//
// out is always the completion SUFFIX (Sample.Output), never req.Buf+out.
// Graders that need the full command line build it explicitly via
// fullCommand — this distinction is the most likely source of a
// silently-wrong grader.

// fullCommand reconstructs the full command line a user would see:
// req.Buf + out, with no separator inserted (mirrors the production
// contract in prompt.systemPrompt — the model supplies its own spacing).
func fullCommand(in protocol.Request, out string) string {
	return in.Buf + out
}

// normalizeSpace collapses all runs of whitespace to a single space and
// trims the ends, so "git  status\n" and "git status" compare equal.
func normalizeSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// ---- Core graders ----------------------------------------------------

// MatchesRegexp reports whether out matches pattern. pattern is compiled
// once at construction time (init-time, effectively), so a malformed
// pattern panics immediately at case-corpus construction rather than
// surfacing as a per-sample grader error buried in a scorecard.
func MatchesRegexp(name, pattern string) Grader {
	re := regexp.MustCompile(pattern)
	return GraderFunc{N: name, F: func(_ protocol.Request, out string) (bool, error) {
		return re.MatchString(out), nil
	}}
}

// Contains reports whether out contains sub, case-insensitively.
func Contains(sub string) Grader {
	low := strings.ToLower(sub)
	return GraderFunc{N: "contains:" + sub, F: func(_ protocol.Request, out string) (bool, error) {
		return strings.Contains(strings.ToLower(out), low), nil
	}}
}

// ContainsAny reports whether out contains any of subs, case-insensitively.
func ContainsAny(subs ...string) Grader {
	lows := make([]string, len(subs))
	for i, s := range subs {
		lows[i] = strings.ToLower(s)
	}
	return GraderFunc{N: "contains-any:" + strings.Join(subs, "|"), F: func(_ protocol.Request, out string) (bool, error) {
		low := strings.ToLower(out)
		for _, s := range lows {
			if strings.Contains(low, s) {
				return true, nil
			}
		}
		return false, nil
	}}
}

// HasLeadingSpace reports whether out starts with a space character. This is
// the exact spacing contract prompt.systemPrompt documents: a completion
// starting a new word must supply its own leading space.
func HasLeadingSpace() Grader {
	return GraderFunc{N: "has-leading-space", F: func(_ protocol.Request, out string) (bool, error) {
		return len(out) > 0 && out[0] == ' ', nil
	}}
}

// IsEmpty reports whether out is empty once leading/trailing whitespace is
// trimmed (Runner already trims trailing whitespace, but a case grading raw
// stub scripts or future callers shouldn't rely on that).
func IsEmpty() Grader {
	return GraderFunc{N: "is-empty", F: func(_ protocol.Request, out string) (bool, error) {
		return strings.TrimSpace(out) == "", nil
	}}
}

// LongerThan reports whether out has more than n runes (not bytes — a
// completion with multibyte characters must not be miscounted short).
func LongerThan(n int) Grader {
	return GraderFunc{N: "longer-than", F: func(_ protocol.Request, out string) (bool, error) {
		return len([]rune(out)) > n, nil
	}}
}

// ContainsNewline reports whether out contains a newline. This should almost
// never fire in practice: provider.Complete's accumulator already cuts the
// stream at the first newline (accum.go), so this grader exists to GUARD
// that cutoff, not to catch routine multi-line output. Don't delete it as
// dead weight if it never trips — a trip is the interesting case.
func ContainsNewline() Grader {
	return GraderFunc{N: "contains-newline", F: func(_ protocol.Request, out string) (bool, error) {
		return strings.ContainsAny(out, "\n\r"), nil
	}}
}

// fenceOrBacktickRe matches a single backtick, a triple-backtick fence, or a
// tilde fence.
var fenceOrBacktickRe = regexp.MustCompile("`|~~~")

// ContainsBacktickOrFence reports whether out contains a backtick or a
// markdown code fence — a sign the model reverted to chat-assistant
// formatting instead of a raw shell completion.
func ContainsBacktickOrFence() Grader {
	return GraderFunc{N: "contains-backtick-or-fence", F: func(_ protocol.Request, out string) (bool, error) {
		return fenceOrBacktickRe.MatchString(out), nil
	}}
}

// proseMarkers are case-insensitive substrings that indicate the model
// answered in prose instead of emitting a shell completion. "i " / " i " is
// deliberately word-boundary matched (via proseIRe) so it doesn't fire
// inside "git", "unix", etc.
var proseSubstrMarkers = []string{
	"sorry", "cannot", "not enough", "unable", "as an ai", "here is", "note:",
}

// proseIRe matches a standalone "I" (word-boundary), case-insensitively —
// e.g. "I can't" — without matching the "i" inside "git" or "wait".
var proseIRe = regexp.MustCompile(`(?i)\bi\b`)

// LooksLikeProse reports whether out contains any of the plan's prose
// markers (sorry, cannot, not enough, unable, as an ai, here is, note:) or a
// standalone "I".
func LooksLikeProse() Grader {
	return GraderFunc{N: "looks-like-prose", F: func(_ protocol.Request, out string) (bool, error) {
		low := strings.ToLower(out)
		for _, m := range proseSubstrMarkers {
			if strings.Contains(low, m) {
				return true, nil
			}
		}
		return proseIRe.MatchString(out), nil
	}}
}

// ---- Context-aware graders --------------------------------------------

// RestatesBuffer reports whether out, appended to req.Buf, merely repeats
// req.Buf's text again — i.e. the suffix re-states the buffer instead of
// extending it. Whitespace is normalized before comparing so "git status"
// vs " git  status" still counts. Only fires when req.Buf is non-empty (an
// empty buffer can't be "restated").
func RestatesBuffer() Grader {
	return GraderFunc{N: "restates-buffer", F: func(in protocol.Request, out string) (bool, error) {
		buf := normalizeSpace(in.Buf)
		if buf == "" {
			return false, nil
		}
		// Prefix, not equality: the defect is the suffix BEGINNING with the
		// buffer's own text ("git statusgit status --short" concatenated).
		// Equality would miss the restate-then-continue form.
		return strings.HasPrefix(normalizeSpace(out), buf), nil
	}}
}

// lastToken returns the last whitespace-separated token of s, trimmed of
// surrounding quotes and common trailing punctuation.
func lastToken(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	tok := fields[len(fields)-1]
	tok = strings.Trim(tok, `"'`)
	return tok
}

// NamesEntryInDirEntries reports whether the last whitespace-separated token
// of out (quotes trimmed) is present in in.DirEntries.
func NamesEntryInDirEntries() Grader {
	return GraderFunc{N: "names-entry-in-dir-entries", F: func(in protocol.Request, out string) (bool, error) {
		tok := lastToken(out)
		if tok == "" {
			return false, nil
		}
		for _, e := range in.DirEntries {
			if e == tok {
				return true, nil
			}
		}
		return false, nil
	}}
}

// filenameShapedRe matches a bare-word token that looks like a filename or
// path: has a dotted extension, or contains a path separator.
var filenameShapedRe = regexp.MustCompile(`^[\w.\-/]*[\w\-]\.[A-Za-z0-9]{1,8}$|^[\w.\-]*/[\w.\-/]+$`)

// NamesPathNotInDirEntries reports whether out contains a filename-shaped
// token (has an extension, or looks like a path) that is NOT in
// in.DirEntries. It only fires when in.DirEntries is non-empty: with no
// listing at all there is nothing for the suggestion to contradict, so an
// empty-context case must never be reported as a fabrication.
func NamesPathNotInDirEntries() Grader {
	return GraderFunc{N: "names-path-not-in-dir-entries", F: func(in protocol.Request, out string) (bool, error) {
		if len(in.DirEntries) == 0 {
			return false, nil
		}
		known := make(map[string]bool, len(in.DirEntries))
		for _, e := range in.DirEntries {
			known[e] = true
		}
		for _, tok := range strings.Fields(out) {
			tok = strings.Trim(tok, `"'`)
			if tok == "" || !filenameShapedRe.MatchString(tok) {
				continue
			}
			if !known[tok] {
				return true, nil
			}
		}
		return false, nil
	}}
}

// contextTokenAllowlist is shell keywords/flags/command names
// ContainsTokenNotInContext must never flag as "invented" — otherwise every
// suggestion trivially "invents" a token via ordinary shell vocabulary.
var contextTokenAllowlist = map[string]bool{
	// common commands
	"git": true, "cd": true, "ls": true, "rm": true, "mv": true, "cp": true,
	"mkdir": true, "cat": true, "echo": true, "npm": true, "go": true,
	"docker": true, "curl": true, "ssh": true, "vim": true, "python": true,
	"python3": true, "make": true, "grep": true, "find": true, "sudo": true,
	"pip": true, "pip3": true, "yarn": true, "brew": true, "source": true,
	"export": true, "chmod": true, "chown": true, "kill": true, "ps": true,
	"tar": true, "touch": true, "less": true, "more": true, "man": true,
	// common git subcommands
	"add": true, "commit": true, "push": true, "pull": true, "status": true,
	"diff": true, "log": true, "branch": true, "switch": true, "checkout": true,
	"merge": true, "rebase": true, "clone": true, "fetch": true, "tag": true,
	"stash": true, "remote": true, "init": true, "reset": true,
	// docker/npm/go subcommands
	"compose": true, "up": true, "down": true, "build": true, "run": true,
	"install": true, "test": true, "start": true, "mod": true, "tidy": true,
	// misc words that show up constantly and carry no invented content
	"origin": true, "main": true, "master": true, "all": true, "true": true,
	"false": true, "null": true, "and": true, "or": true, "not": true,
}

// identifierTokenRe extracts identifier-shaped words (letters/digits plus
// _-./) from out for ContainsTokenNotInContext, skipping pure flags
// (leading '-') and pure numbers.
var identifierTokenRe = regexp.MustCompile(`[A-Za-z0-9_][A-Za-z0-9_./-]*`)

var pureNumberRe = regexp.MustCompile(`^[0-9]+$`)

// ContainsTokenNotInContext reports whether out contains an identifier-shaped
// token absent from ALL of in.History, in.DirEntries, in.GitBranch, and
// in.Buf. Flags (leading '-') and pure numbers are skipped, and
// contextTokenAllowlist excludes shell keywords / common flags / common
// command names, so this fires on genuinely invented identifiers — e.g.
// "git switch -c my-invented-feature" — not on ordinary vocabulary like
// "git switch -c".
func ContainsTokenNotInContext() Grader {
	return GraderFunc{N: "contains-token-not-in-context", F: func(in protocol.Request, out string) (bool, error) {
		known := contextVocabulary(in)
		for _, field := range strings.Fields(out) {
			if strings.HasPrefix(field, "-") {
				continue // flags
			}
			for _, tok := range identifierTokenRe.FindAllString(field, -1) {
				low := strings.ToLower(tok)
				if pureNumberRe.MatchString(tok) {
					continue
				}
				if contextTokenAllowlist[low] {
					continue
				}
				if !known[low] {
					return true, nil
				}
			}
		}
		return false, nil
	}}
}

// contextVocabulary collects every identifier-shaped token that appears
// anywhere in in's history/dir_entries/branch/buf, lowercased, for
// ContainsTokenNotInContext to compare against.
func contextVocabulary(in protocol.Request) map[string]bool {
	known := map[string]bool{}
	add := func(s string) {
		for _, tok := range identifierTokenRe.FindAllString(s, -1) {
			known[strings.ToLower(tok)] = true
		}
	}
	for _, h := range in.History {
		add(h)
	}
	for _, d := range in.DirEntries {
		add(d)
	}
	add(in.GitBranch)
	add(in.Buf)
	return known
}

// urlRe matches an http(s) URL or an scp-style user@host: reference.
var urlRe = regexp.MustCompile(`https?://\S+|[A-Za-z0-9_.\-]+@[A-Za-z0-9_.\-]+:`)

// ContainsURL reports whether out contains an http(s) URL or an scp-style
// user@host: reference.
func ContainsURL() Grader {
	return GraderFunc{N: "contains-url", F: func(_ protocol.Request, out string) (bool, error) {
		return urlRe.MatchString(out), nil
	}}
}

// hostnameShapedRe matches a dotted hostname (e.g. example.com,
// 192.168.1.1) or a token following an '@' (user@host).
var hostnameShapedRe = regexp.MustCompile(`\b[A-Za-z0-9][A-Za-z0-9\-]*(?:\.[A-Za-z0-9][A-Za-z0-9\-]*)+\b|@[A-Za-z0-9][A-Za-z0-9_.\-]*`)

// ContainsHostNotInHistory reports whether out contains a hostname-shaped
// token (dotted, or following '@') that does not appear anywhere in
// in.History.
func ContainsHostNotInHistory() Grader {
	return GraderFunc{N: "contains-host-not-in-history", F: func(in protocol.Request, out string) (bool, error) {
		historyBlob := strings.ToLower(strings.Join(in.History, " "))
		for _, m := range hostnameShapedRe.FindAllString(out, -1) {
			host := strings.ToLower(strings.TrimPrefix(m, "@"))
			if host == "" {
				continue
			}
			if !strings.Contains(historyBlob, host) {
				return true, nil
			}
		}
		return false, nil
	}}
}

// closesQuoteRe matches a quoted span with at least one non-space character
// inside it — i.e. a complete, non-empty quoted string, per the plan doc's
// exact pattern.
var closesQuoteRe = regexp.MustCompile(`"[^"]*\S[^"]*"`)

// ClosesQuoteWithContent reports whether req.Buf+out contains a quoted span
// with non-space content — the model invented free-form input (e.g. a commit
// message) and closed the quote itself. Applied to the FULL command line so
// an opening quote already present in the buffer (buf `git commit -m "`)
// still counts once the suffix closes it.
func ClosesQuoteWithContent() Grader {
	return GraderFunc{N: "closes-quote-with-content", F: func(in protocol.Request, out string) (bool, error) {
		return closesQuoteRe.MatchString(fullCommand(in, out)), nil
	}}
}

// EqualsRecentHistory reports whether the full command (req.Buf+out,
// trimmed and whitespace-normalized) equals one of the last n entries of
// in.History.
func EqualsRecentHistory(n int) Grader {
	return GraderFunc{N: "equals-recent-history", F: func(in protocol.Request, out string) (bool, error) {
		full := normalizeSpace(fullCommand(in, out))
		if full == "" {
			return false, nil
		}
		recent := in.History
		if n >= 0 && len(recent) > n {
			recent = recent[len(recent)-n:]
		}
		for _, h := range recent {
			if normalizeSpace(h) == full {
				return true, nil
			}
		}
		return false, nil
	}}
}

// InHistory reports whether the full command (req.Buf+out) equals ANY entry
// in in.History.
func InHistory() Grader {
	return GraderFunc{N: "in-history", F: func(in protocol.Request, out string) (bool, error) {
		full := normalizeSpace(fullCommand(in, out))
		if full == "" {
			return false, nil
		}
		for _, h := range in.History {
			if normalizeSpace(h) == full {
				return true, nil
			}
		}
		return false, nil
	}}
}

// digitRunRe matches a run of one or more digits, for
// EqualsHistoryModuloNumber's normalization.
var digitRunRe = regexp.MustCompile(`[0-9]+`)

// normalizeDigits replaces every run of digits in s with a fixed
// placeholder, so "v1.1.0" and "v1.2.0" normalize identically.
func normalizeDigits(s string) string {
	return digitRunRe.ReplaceAllString(s, "#")
}

// EqualsHistoryModuloNumber reports whether the full command equals a
// history entry after normalizing every run of digits to a placeholder, BUT
// is not byte-identical (whitespace-normalized) to that entry. This is the
// "nonsense incrementing" check (C1/C2): git tag v1.1.0 -> git tag v1.2.0
// fires (same shape, different number); git tag v1.1.0 -> git tag v1.1.0
// does not (that's a plain repeat — category D's business, not this one).
func EqualsHistoryModuloNumber() Grader {
	return GraderFunc{N: "equals-history-modulo-number", F: func(in protocol.Request, out string) (bool, error) {
		full := normalizeSpace(fullCommand(in, out))
		if full == "" {
			return false, nil
		}
		normFull := normalizeDigits(full)
		for _, h := range in.History {
			normH := normalizeSpace(h)
			if normFull != normalizeDigits(normH) {
				continue
			}
			if normH != full { // not a byte-identical repeat
				return true, nil
			}
		}
		return false, nil
	}}
}

// topLevelSeparatorRe matches ';' or '&&' anywhere in the string. It does
// not attempt real shell parsing (no quote-awareness) — the plan's category
// A cases are short enough that this is deliberately simple; a
// quote-aware version can be added if a false positive shows up.
var topLevelSeparatorRe = regexp.MustCompile(`;|&&`)

// ContainsSeparator reports whether out contains a top-level ';' or '&&'
// anywhere — i.e. the model over-chained commands (A4).
func ContainsSeparator() Grader {
	return GraderFunc{N: "contains-separator", F: func(_ protocol.Request, out string) (bool, error) {
		return topLevelSeparatorRe.MatchString(out), nil
	}}
}

// startsWithSeparatorRe matches a leading (after optional whitespace) &&,
// ||, |, or ; — the exact pattern the plan gives for A1.
var startsWithSeparatorRe = regexp.MustCompile(`^\s*(&&|\|\|?|;)`)

// StartsWithSeparator reports whether out starts (after optional leading
// whitespace) with &&, ||, |, or ; — a next-command prediction that leads
// with a separator instead of a bare command.
func StartsWithSeparator() Grader {
	return GraderFunc{N: "starts-with-separator", F: func(_ protocol.Request, out string) (bool, error) {
		return startsWithSeparatorRe.MatchString(out), nil
	}}
}

// topLevelCommandAllowlist is tokens that plausibly START a standalone shell
// command. ContinuesLastHistoryCommand uses it to tell "the model emitted a
// NEW command" from "the model emitted bare arguments that only make sense
// concatenated onto the previous history line".
var topLevelCommandAllowlist = map[string]bool{
	"git": true, "cd": true, "ls": true, "rm": true, "mv": true, "cp": true,
	"mkdir": true, "cat": true, "echo": true, "npm": true, "go": true,
	"docker": true, "curl": true, "ssh": true, "vim": true, "python": true,
	"python3": true, "make": true, "grep": true, "find": true, "sudo": true,
	"pip": true, "pip3": true, "yarn": true, "brew": true, "source": true,
	"export": true, "chmod": true, "chown": true, "kill": true, "ps": true,
	"tar": true, "touch": true, "less": true, "more": true, "man": true,
}

// argumentlessGitSubcommands are git subcommands that are already complete,
// valid commands with no positional arguments — exactly the shape whose
// history line invites the FIM contiguity bug ContinuesLastHistoryCommand
// guards against.
var argumentlessGitSubcommands = map[string]bool{
	"push": true, "pull": true, "fetch": true, "status": true, "log": true,
	"diff": true, "add": true,
}

// ContinuesLastHistoryCommand reports whether out looks like bare arguments
// continuing the LAST history entry rather than a standalone new command:
// the entry's first two words are "git" plus an argumentless subcommand
// (e.g. "git push", already complete on its own), yet out's first token is
// not a recognized command word. This is the FIM contiguity failure mode:
// with nothing marking the boundary between history and the predicted next
// command, the model can treat "git push" as unfinished and append
// "origin main" — fine concatenated, not a command on its own line.
func ContinuesLastHistoryCommand() Grader {
	return GraderFunc{N: "continues-last-history-command", F: func(in protocol.Request, out string) (bool, error) {
		if len(in.History) == 0 {
			return false, nil
		}
		last := strings.Fields(in.History[len(in.History)-1])
		if len(last) < 2 || last[0] != "git" || !argumentlessGitSubcommands[last[1]] {
			return false, nil
		}
		suggestion := strings.Fields(out)
		if len(suggestion) == 0 {
			return false, nil
		}
		return !topLevelCommandAllowlist[suggestion[0]], nil
	}}
}

// ---- Composition --------------------------------------------------------

// AnyOf reports whether ANY of gs is present, short-circuiting on the first
// present result (in order) or the first grader error. Prefer separate
// assertions over AnyOf/AllOf wherever the plan describes independent
// checks — a compound grader that fails tells you less than two that fail
// separately. Use AnyOf only for a genuine disjunction (e.g. F1's "empty or
// short").
func AnyOf(name string, gs ...Grader) Grader {
	return CtxGraderFunc{N: name, F: func(ctx context.Context, in protocol.Request, out string) (bool, error) {
		for _, g := range gs {
			ok, err := g.Grade(ctx, in, out)
			if err != nil {
				return false, err
			}
			if ok {
				return true, nil
			}
		}
		return false, nil
	}}
}

// AllOf reports whether ALL of gs are present. See AnyOf's doc comment on
// when compound graders are (and mostly aren't) the right call.
func AllOf(name string, gs ...Grader) Grader {
	return CtxGraderFunc{N: name, F: func(ctx context.Context, in protocol.Request, out string) (bool, error) {
		for _, g := range gs {
			ok, err := g.Grade(ctx, in, out)
			if err != nil {
				return false, err
			}
			if !ok {
				return false, nil
			}
		}
		return true, nil
	}}
}

// Not inverts g's presence: it reports true exactly when g does not. It's
// small enough not to need its own bullet in the plan doc's grader list, but
// F1 needs "not longer than 8 chars" as one leg of an AnyOf, and negating
// LongerThan is more honest than adding a parallel NotLongerThan primitive.
func Not(g Grader) Grader {
	return CtxGraderFunc{N: "not-" + g.Name(), F: func(ctx context.Context, in protocol.Request, out string) (bool, error) {
		ok, err := g.Grade(ctx, in, out)
		if err != nil {
			return false, err
		}
		return !ok, nil
	}}
}
