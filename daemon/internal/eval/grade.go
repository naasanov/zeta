package eval

import (
	"context"
	"path"
	"regexp"
	"strings"

	"github.com/naasanov/zsh-autopilot/daemon/internal/protocol"
)

func fullCommand(in protocol.Request, out string) string {
	return in.Buf + out
}

func normalizeSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// ---- Core graders ----------------------------------------------------

func MatchesRegexp(name, pattern string) Grader {
	re := regexp.MustCompile(pattern)
	return GraderFunc{N: name, F: func(_ protocol.Request, out string) (bool, error) {
		return re.MatchString(out), nil
	}}
}

func Contains(sub string) Grader {
	low := strings.ToLower(sub)
	return GraderFunc{N: "contains:" + sub, F: func(_ protocol.Request, out string) (bool, error) {
		return strings.Contains(strings.ToLower(out), low), nil
	}}
}

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

func HasLeadingSpace() Grader {
	return GraderFunc{N: "has-leading-space", F: func(_ protocol.Request, out string) (bool, error) {
		return len(out) > 0 && out[0] == ' ', nil
	}}
}

func IsEmpty() Grader {
	return GraderFunc{N: "is-empty", F: func(_ protocol.Request, out string) (bool, error) {
		return strings.TrimSpace(out) == "", nil
	}}
}

func LongerThan(n int) Grader {
	return GraderFunc{N: "longer-than", F: func(_ protocol.Request, out string) (bool, error) {
		return len([]rune(out)) > n, nil
	}}
}

func ContainsNewline() Grader {
	return GraderFunc{N: "contains-newline", F: func(_ protocol.Request, out string) (bool, error) {
		return strings.ContainsAny(out, "\n\r"), nil
	}}
}

var fenceOrBacktickRe = regexp.MustCompile("`|~~~")

func ContainsBacktickOrFence() Grader {
	return GraderFunc{N: "contains-backtick-or-fence", F: func(_ protocol.Request, out string) (bool, error) {
		return fenceOrBacktickRe.MatchString(out), nil
	}}
}

var proseSubstrMarkers = []string{
	"sorry", "cannot", "not enough", "unable", "as an ai", "here is", "note:",
}

var proseIRe = regexp.MustCompile(`(?i)\bi\b`)

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

func RestatesBuffer() Grader {
	return GraderFunc{N: "restates-buffer", F: func(in protocol.Request, out string) (bool, error) {
		buf := normalizeSpace(in.Buf)
		if buf == "" {
			return false, nil
		}
		return strings.HasPrefix(normalizeSpace(out), buf), nil
	}}
}

func lastToken(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	tok := fields[len(fields)-1]
	tok = strings.Trim(tok, `"'`)
	return tok
}

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

var filenameShapedRe = regexp.MustCompile(`^[\w.\-/]*[\w\-]\.[A-Za-z0-9]{1,8}$|^[\w.\-]*/[\w.\-/]+$`)

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

var contextTokenAllowlist = map[string]bool{
	"git": true, "cd": true, "ls": true, "rm": true, "mv": true, "cp": true,
	"mkdir": true, "cat": true, "echo": true, "npm": true, "go": true,
	"docker": true, "curl": true, "ssh": true, "vim": true, "python": true,
	"python3": true, "make": true, "grep": true, "find": true, "sudo": true,
	"pip": true, "pip3": true, "yarn": true, "brew": true, "source": true,
	"export": true, "chmod": true, "chown": true, "kill": true, "ps": true,
	"tar": true, "touch": true, "less": true, "more": true, "man": true,
	"add": true, "commit": true, "push": true, "pull": true, "status": true,
	"diff": true, "log": true, "branch": true, "switch": true, "checkout": true,
	"merge": true, "rebase": true, "clone": true, "fetch": true, "tag": true,
	"stash": true, "remote": true, "init": true, "reset": true,
	"compose": true, "up": true, "down": true, "build": true, "run": true,
	"install": true, "test": true, "start": true, "mod": true, "tidy": true,
	"origin": true, "main": true, "master": true, "all": true, "true": true,
	"false": true, "null": true, "and": true, "or": true, "not": true,
}

// identifierTokenRe excludes '/' so a path or URL splits into segments.
var identifierTokenRe = regexp.MustCompile(`[A-Za-z0-9_][A-Za-z0-9_.-]*`)

var pureNumberRe = regexp.MustCompile(`^[0-9]+$`)

func knownToken(low string, known map[string]bool) bool {
	if known[low] {
		return true
	}
	trimmed, ok := strings.CutSuffix(low, ".git")
	return ok && known[trimmed]
}

func ContainsTokenNotInContext() Grader {
	return GraderFunc{N: "contains-token-not-in-context", F: func(in protocol.Request, out string) (bool, error) {
		known := contextVocabulary(in)
		for _, field := range strings.Fields(out) {
			if strings.HasPrefix(field, "-") {
				continue
			}
			for _, tok := range identifierTokenRe.FindAllString(field, -1) {
				low := strings.ToLower(tok)
				if pureNumberRe.MatchString(tok) {
					continue
				}
				if contextTokenAllowlist[low] {
					continue
				}
				if !knownToken(low, known) {
					return true, nil
				}
			}
		}
		return false, nil
	}}
}

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

var urlRe = regexp.MustCompile(`https?://\S+|[A-Za-z0-9_.\-]+@[A-Za-z0-9_.\-]+:`)

func ContainsURL() Grader {
	return GraderFunc{N: "contains-url", F: func(_ protocol.Request, out string) (bool, error) {
		return urlRe.MatchString(out), nil
	}}
}

// urlAuthorityRe stops before ':' so the port is excluded.
var urlAuthorityRe = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.\-]*://(?:[^/@\s]*@)?([A-Za-z0-9_][A-Za-z0-9_.\-]*)`)

var atHostRe = regexp.MustCompile(`@([A-Za-z0-9_][A-Za-z0-9_.\-]*)`)

var hostPortRe = regexp.MustCompile(`\b([A-Za-z0-9_][A-Za-z0-9_.\-]*):[0-9]{2,5}\b`)

var dottedHostRe = regexp.MustCompile(`\b[A-Za-z0-9][A-Za-z0-9\-]*(?:\.[A-Za-z0-9][A-Za-z0-9\-]*)+\b`)

var urlOrScpSpanRe = regexp.MustCompile(`https?://\S+|[A-Za-z0-9_.\-]+@[A-Za-z0-9_.\-]+:\S*`)

// hostRefs: a pure number is never a host (e.g. "12:30" would read as host 12, port 30).
func hostRefs(s string) []string {
	var hosts []string
	seen := map[string]bool{}
	add := func(h string) {
		h = strings.ToLower(strings.Trim(h, "."))
		if h == "" || pureNumberRe.MatchString(h) || seen[h] {
			return
		}
		seen[h] = true
		hosts = append(hosts, h)
	}
	for _, re := range []*regexp.Regexp{urlAuthorityRe, atHostRe, hostPortRe} {
		for _, m := range re.FindAllStringSubmatch(s, -1) {
			add(m[1])
		}
	}
	for _, m := range dottedHostRe.FindAllString(urlOrScpSpanRe.ReplaceAllString(s, " "), -1) {
		add(m)
	}
	return hosts
}

func ContainsHostNotInHistory() Grader {
	return GraderFunc{N: "contains-host-not-in-history", F: func(in protocol.Request, out string) (bool, error) {
		historyBlob := strings.ToLower(strings.Join(in.History, " "))
		for _, host := range hostRefs(out) {
			if !strings.Contains(historyBlob, host) {
				return true, nil
			}
		}
		return false, nil
	}}
}

var closesQuoteRe = regexp.MustCompile(`"[^"]*\S[^"]*"`)

func ClosesQuoteWithContent() Grader {
	return GraderFunc{N: "closes-quote-with-content", F: func(in protocol.Request, out string) (bool, error) {
		return closesQuoteRe.MatchString(fullCommand(in, out)), nil
	}}
}

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

var digitRunRe = regexp.MustCompile(`[0-9]+`)

func normalizeDigits(s string) string {
	return digitRunRe.ReplaceAllString(s, "#")
}

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
			if normH != full {
				return true, nil
			}
		}
		return false, nil
	}}
}

var topLevelSeparatorRe = regexp.MustCompile(`;|&&`)

func ContainsSeparator() Grader {
	return GraderFunc{N: "contains-separator", F: func(_ protocol.Request, out string) (bool, error) {
		return topLevelSeparatorRe.MatchString(out), nil
	}}
}

var startsWithSeparatorRe = regexp.MustCompile(`^\s*(&&|\|\|?|;)`)

func StartsWithSeparator() Grader {
	return GraderFunc{N: "starts-with-separator", F: func(_ protocol.Request, out string) (bool, error) {
		return startsWithSeparatorRe.MatchString(out), nil
	}}
}

var topLevelCommandAllowlist = map[string]bool{
	"git": true, "cd": true, "ls": true, "rm": true, "mv": true, "cp": true,
	"mkdir": true, "cat": true, "echo": true, "npm": true, "go": true,
	"docker": true, "curl": true, "ssh": true, "vim": true, "python": true,
	"python3": true, "make": true, "grep": true, "find": true, "sudo": true,
	"pip": true, "pip3": true, "yarn": true, "brew": true, "source": true,
	"export": true, "chmod": true, "chown": true, "kill": true, "ps": true,
	"tar": true, "touch": true, "less": true, "more": true, "man": true,
}

var argumentlessGitSubcommands = map[string]bool{
	"push": true, "pull": true, "fetch": true, "status": true, "log": true,
	"diff": true, "add": true,
}

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

// ---- cwd-aware graders (context, cwd-scoped prompts) --------------------

func EchoesOtherCwdCommand() Grader {
	return GraderFunc{N: "echoes-other-cwd-command", F: func(in protocol.Request, out string) (bool, error) {
		outFields := strings.Fields(out)
		if len(outFields) == 0 {
			return false, nil
		}
		for _, e := range in.HistoryWithCwd() {
			if e.Cwd == "" || e.Cwd == in.Cwd {
				continue
			}
			histFields := strings.Fields(e.Cmd)
			if len(histFields) == 0 || outFields[0] != histFields[0] {
				continue
			}
			if len(outFields) > 1 && len(histFields) > 1 && outFields[1] != histFields[1] {
				continue
			}
			return true, nil
		}
		return false, nil
	}}
}

func ContainsOtherCwdOnlyToken() Grader {
	return GraderFunc{N: "contains-other-cwd-only-token", F: func(in protocol.Request, out string) (bool, error) {
		local := map[string]bool{}
		foreign := map[string]bool{}
		add := func(m map[string]bool, s string) {
			for _, tok := range identifierTokenRe.FindAllString(s, -1) {
				m[strings.ToLower(tok)] = true
			}
		}
		hasOtherCwd := false
		for _, e := range in.HistoryWithCwd() {
			if e.Cwd != "" && e.Cwd != in.Cwd {
				hasOtherCwd = true
				add(foreign, e.Cmd)
			} else {
				add(local, e.Cmd)
			}
		}
		if !hasOtherCwd {
			return false, nil
		}
		for _, d := range in.DirEntries {
			add(local, d)
		}
		add(local, in.GitBranch)
		add(local, in.Buf)

		for _, field := range strings.Fields(out) {
			if strings.HasPrefix(field, "-") {
				continue
			}
			for _, tok := range identifierTokenRe.FindAllString(field, -1) {
				low := strings.ToLower(tok)
				if pureNumberRe.MatchString(tok) || contextTokenAllowlist[low] {
					continue
				}
				if foreign[low] && !knownToken(low, local) {
					return true, nil
				}
			}
		}
		return false, nil
	}}
}

func OutputMentionsParentDirArtifact() Grader {
	return GraderFunc{N: "output-mentions-parent-dir-artifact", F: func(in protocol.Request, out string) (bool, error) {
		if in.Cwd == "" || out == "" {
			return false, nil
		}
		parent := path.Dir(in.Cwd)
		if parent == "." || parent == in.Cwd {
			return false, nil
		}
		if strings.Contains(out, "..") {
			return true, nil
		}
		return strings.Contains(out, parent), nil
	}}
}

// ---- Composition --------------------------------------------------------

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

func Not(g Grader) Grader {
	return CtxGraderFunc{N: "not-" + g.Name(), F: func(ctx context.Context, in protocol.Request, out string) (bool, error) {
		ok, err := g.Grade(ctx, in, out)
		if err != nil {
			return false, err
		}
		return !ok, nil
	}}
}
