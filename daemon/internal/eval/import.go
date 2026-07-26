// This file is Part 3b of .docs/eval_harness_plan.md: the importer that turns
// real dogfooding failures (the §12 metrics log, raw-text capture — see
// CLAUDE.md "Metrics (design §12)") into eval Cases. It reads events.jsonl,
// reconstructs the protocol.Request that produced each "request" event, and
// recovers the completion suffix the model actually emitted, so a bad
// suggestion hit while dogfooding becomes a regression guard instead of an
// anecdote.
//
// # Why this file does not import internal/metrics
//
// internal/metrics is deliberately a removable leaf (see its package doc and
// CLAUDE.md: "stripping metrics = deleting the directory, then revert the
// lines tagged METRICS(§12)"). If internal/eval imported it, metrics could no
// longer be deleted in one step — the eval package would be a second place
// that breaks. rawRequestEvent below is a local, minimal mirror of the JSON
// shape metrics.RequestEvent produces (same field tags, only the subset this
// importer needs), so this package depends on the WIRE FORMAT, not the Go
// type. If the metrics JSON shape changes, this file needs an update either
// way; it just doesn't need metrics.go to compile.
//
// # Privacy — not redaction
//
// events.jsonl currently holds unredacted command lines (raw-text capture is
// temporarily default-ON for the dogfooding window; Phase-3 secret redaction
// is not built yet — see CLAUDE.md). SkipLikelySecrets below is a best-effort
// heuristic filter, NOT redaction: it catches common secret shapes (cloud
// access key prefixes, API-key-style tokens, PEM blocks, key=value
// assignments, long base64/hex runs) so an import run doesn't casually
// reproduce a credential into a stub file. It will miss secrets that don't
// match any pattern (a bespoke internal token format, a plain-English
// password with no distinguishing shape, a secret split across an
// unassuming-looking argument). A human MUST read every rendered stub before
// pasting it into cases.go — this filter reduces the odds of an accidental
// leak, it does not eliminate them.
package eval

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/format"
	"io"
	"regexp"
	"strings"

	"github.com/naasanov/zsh-autopilot/daemon/internal/protocol"
)

// rawRequestEvent mirrors the JSON shape of metrics.RequestEvent, restricted
// to the fields this importer needs. See the package-doc comment above for
// why this is a local copy rather than an import of internal/metrics.
type rawRequestEvent struct {
	Event     string `json:"event"`
	RequestID string `json:"request_id"`
	Trigger   string `json:"trigger"` // "typing" | "next_command"
	Provider  string `json:"provider"`
	Model     string `json:"model"`

	// Raw-text fields, present only when raw-text capture is on (all
	// omitempty on the writer side, so a row missing all of them was
	// captured without raw text and carries nothing to import).
	Buf        string   `json:"buf,omitempty"`
	Suggestion string   `json:"suggestion,omitempty"`
	Cwd        string   `json:"cwd,omitempty"`
	GitBranch  string   `json:"git_branch,omitempty"`
	GitDirty   bool     `json:"git_dirty,omitempty"`
	LastExit   int      `json:"last_exit,omitempty"`
	History    []string `json:"history,omitempty"`
	DirEntries []string `json:"dir_entries,omitempty"`
}

// ImportedCase is one harvested request plus what the model actually said.
type ImportedCase struct {
	Case       Case   // Req reconstructed; Asserts empty — a human adds those
	Suggestion string // the completion SUFFIX the model produced
	Provider   string
	Model      string
	RequestID  string
}

// ImportOptions controls ImportEvents' filtering, capping, and privacy
// behavior.
type ImportOptions struct {
	// TriggerFilter restricts import to one trigger kind ("typing" or
	// "next_command", matching protocol.KindTyping/KindNextCommand). Empty
	// string means no filtering — import both kinds.
	TriggerFilter string

	// MaxCases caps the number of ImportedCase values returned (after
	// dedup). Zero means unlimited. Rows beyond the cap are still counted in
	// ImportStats (they're skipped, not silently truncated from the stats).
	MaxCases int

	// SkipLikelySecrets drops rows whose buf/suggestion/history match common
	// secret shapes. Default-ON: callers must opt OUT explicitly, because
	// the safe default for a privacy-sensitive importer is "skip", not
	// "import". See the package doc above: this is a heuristic, not
	// redaction. Use NewImportOptions (or just the zero value) to get the
	// safe default; setting this field directly to false is how a caller
	// opts out, and should be a deliberate, reviewed choice.
	SkipLikelySecrets bool

	// secretsExplicitlySet distinguishes "zero value, apply the default" from
	// "caller explicitly chose false" when ImportOptions is constructed via
	// a struct literal rather than NewImportOptions. See DefaultImportOptions.
	secretsExplicitlySet bool
}

// DefaultImportOptions returns the safe default: no trigger filter, no cap,
// secrets skipped. Callers building ImportOptions via a struct literal (e.g.
// ImportOptions{TriggerFilter: "typing"}) get SkipLikelySecrets=false unless
// they set it explicitly, since Go has no way to distinguish "unset" from
// "false" on a bare bool field — so this constructor exists specifically for
// callers who want the on-by-default privacy behavior without repeating
// `SkipLikelySecrets: true` (and risking someone deleting that line without
// realizing what it disables).
func DefaultImportOptions() ImportOptions {
	return ImportOptions{SkipLikelySecrets: true, secretsExplicitlySet: true}
}

// ImportStats reports what happened to every line read, so a near-total drop
// of the log is visible rather than indistinguishable from a small, healthy
// import.
type ImportStats struct {
	LinesRead int

	SkippedNotRequest    int // event != "request"
	SkippedNoRawText     int // no raw-text fields present
	SkippedMalformedJSON int // line did not parse as JSON at all
	SkippedBadPrefix     int // suggestion did not start with buf
	SkippedTriggerFilter int // trigger didn't match TriggerFilter
	SkippedLikelySecret  int // matched a secret heuristic
	SkippedOverCap       int // would-be case dropped only because of MaxCases

	Duplicates int // rows that hashed identical to an already-kept case

	Imported int // len(result) — cases actually returned
}

// secretPatterns are best-effort shapes for common credential formats. See
// the package doc: this is NOT redaction, and a human must review every
// stub before it lands in a committed file.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),                                                  // AWS access key id
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}`),                                           // OpenAI/Stripe-style secret key
	regexp.MustCompile(`\bghp_[A-Za-z0-9]{20,}`),                                            // GitHub personal access token
	regexp.MustCompile(`\bgho_[A-Za-z0-9]{20,}`),                                            // GitHub OAuth token
	regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}`),                                    // Slack tokens
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),                                // PEM private key
	regexp.MustCompile(`(?i)\b(password|passwd|pwd|token|api[_-]?key|secret)\s*[:=]\s*\S+`), // key=value assignments
	regexp.MustCompile(`\b[A-Za-z0-9+/]{32,}={0,2}\b`),                                      // long base64-ish run
	regexp.MustCompile(`\b[0-9a-fA-F]{32,}\b`),                                              // long hex run
}

// looksLikeSecret reports whether any of ss contains a common secret shape.
func looksLikeSecret(ss ...string) bool {
	for _, s := range ss {
		if s == "" {
			continue
		}
		for _, re := range secretPatterns {
			if re.MatchString(s) {
				return true
			}
		}
	}
	return false
}

// ImportEvents reads newline-delimited §12 metrics events from r, reconstructs
// a protocol.Request plus the observed completion suffix for every importable
// "request" row, deduplicates, and returns the harvested cases plus stats on
// every row that did NOT make it in.
//
// opts.SkipLikelySecrets defaults to true only when opts was built via
// DefaultImportOptions; a bare ImportOptions{} (or one built with only some
// fields set) defaults to false for that field, matching normal Go zero-value
// semantics — callers who want the safe default should start from
// DefaultImportOptions().
func ImportEvents(r io.Reader, opts ImportOptions) ([]ImportedCase, ImportStats, error) {
	var stats ImportStats
	var result []ImportedCase
	seen := make(map[string]struct{})

	sc := bufio.NewScanner(r)
	// Command lines / history / dir listings can be long; grow the buffer
	// well past bufio's 64KiB default so a wide line doesn't get silently
	// truncated or mistaken for a parse error.
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 8*1024*1024)

	for sc.Scan() {
		line := sc.Bytes()
		stats.LinesRead++
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}

		var ev rawRequestEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			stats.SkippedMalformedJSON++
			continue
		}

		if ev.Event != "request" {
			stats.SkippedNotRequest++
			continue
		}

		hasRawText := ev.Buf != "" || ev.Suggestion != "" || ev.Cwd != "" ||
			ev.GitBranch != "" || ev.GitDirty || ev.LastExit != 0 ||
			len(ev.History) > 0 || len(ev.DirEntries) > 0
		if !hasRawText {
			stats.SkippedNoRawText++
			continue
		}

		if opts.TriggerFilter != "" && ev.Trigger != opts.TriggerFilter {
			stats.SkippedTriggerFilter++
			continue
		}

		if !strings.HasPrefix(ev.Suggestion, ev.Buf) {
			stats.SkippedBadPrefix++
			continue
		}
		suffix := ev.Suggestion[len(ev.Buf):]

		if opts.SkipLikelySecrets {
			if looksLikeSecret(ev.Buf, ev.Suggestion, ev.Cwd, ev.GitBranch, strings.Join(ev.History, "\n"), strings.Join(ev.DirEntries, "\n")) {
				stats.SkippedLikelySecret++
				continue
			}
		}

		req := protocol.Request{
			Kind:       ev.Trigger,
			Buf:        ev.Buf,
			Cwd:        ev.Cwd,
			GitBranch:  ev.GitBranch,
			GitDirty:   ev.GitDirty,
			LastExit:   ev.LastExit,
			History:    ev.History,
			DirEntries: ev.DirEntries,
		}

		key := dedupKey(req, suffix)
		if _, dup := seen[key]; dup {
			stats.Duplicates++
			continue
		}
		seen[key] = struct{}{}

		if opts.MaxCases > 0 && len(result) >= opts.MaxCases {
			stats.SkippedOverCap++
			continue
		}

		result = append(result, ImportedCase{
			Case: Case{
				Req: req,
			},
			Suggestion: suffix,
			Provider:   ev.Provider,
			Model:      ev.Model,
			RequestID:  ev.RequestID,
		})
	}
	if err := sc.Err(); err != nil {
		return result, stats, err
	}

	stats.Imported = len(result)
	return result, stats, nil
}

// dedupKey hashes the reconstructed request plus the observed suggestion so
// identical (request, output) pairs collapse to one case regardless of which
// request_id produced them. Hashing the request struct via JSON rather than
// comparing Go values directly keeps the key stable and independent of slice
// identity/ordering-sensitive equality quirks, and keeps this function simple
// if protocol.Request grows fields later (they just join the hash input).
func dedupKey(req protocol.Request, suggestion string) string {
	b, _ := json.Marshal(req) // Request is plain data; Marshal cannot fail here
	h := sha256.New()
	h.Write(b)
	h.Write([]byte{0})
	h.Write([]byte(suggestion))
	return hex.EncodeToString(h.Sum(nil))
}

// RenderCaseStub emits compilable Go source for one harvested case: a Case
// literal with the reconstructed Req and a TODO for the assertions, plus a
// comment showing the observed suggestion so a reviewer can see what was
// wrong without cross-referencing events.jsonl. id becomes the Case's ID
// field and the local variable name is derived from it, so the caller (or a
// human editing the paste) can rename freely.
//
// The output is a single case entry, formatted as a Go composite literal
// statement — NOT a full source file — meant to be pasted inside one of the
// []Case{...} slices in cases.go. Callers that want a standalone,
// parser-checked fragment (as import_test.go does, to validate via
// go/parser) should wrap it in a minimal package+func shell first; see the
// test for the pattern.
func RenderCaseStub(w io.Writer, ic ImportedCase, id string) error {
	var b strings.Builder

	fmt.Fprintf(&b, "// Imported from dogfooding (request_id=%s, provider=%s, model=%s).\n", ic.RequestID, ic.Provider, ic.Model)
	fmt.Fprintf(&b, "// Observed suggestion (the model's completion SUFFIX): %q\n", ic.Suggestion)
	b.WriteString("// A human must review this stub for secrets before committing it —\n")
	b.WriteString("// SkipLikelySecrets is a heuristic filter, not redaction.\n")
	fmt.Fprintf(&b, "{\n\tID:       %q,\n", id)
	b.WriteString("\tCategory: \"TODO\",\n")
	b.WriteString("\tReq: protocol.Request{\n")

	req := ic.Case.Req
	if req.Kind != "" {
		fmt.Fprintf(&b, "\t\tKind: %s,\n", kindConstant(req.Kind))
	}
	if req.Buf != "" {
		fmt.Fprintf(&b, "\t\tBuf: %q,\n", req.Buf)
	}
	if req.Cwd != "" {
		fmt.Fprintf(&b, "\t\tCwd: %q,\n", req.Cwd)
	}
	if req.GitBranch != "" {
		fmt.Fprintf(&b, "\t\tGitBranch: %q,\n", req.GitBranch)
	}
	if req.GitDirty {
		b.WriteString("\t\tGitDirty: true,\n")
	}
	if req.LastExit != 0 {
		fmt.Fprintf(&b, "\t\tLastExit: %d,\n", req.LastExit)
	}
	if len(req.History) > 0 {
		b.WriteString("\t\tHistory: []string{\n")
		for _, h := range req.History {
			fmt.Fprintf(&b, "\t\t\t%q,\n", h)
		}
		b.WriteString("\t\t},\n")
	}
	if len(req.DirEntries) > 0 {
		b.WriteString("\t\tDirEntries: []string{\n")
		for _, d := range req.DirEntries {
			fmt.Fprintf(&b, "\t\t\t%q,\n", d)
		}
		b.WriteString("\t\t},\n")
	}

	b.WriteString("\t},\n")
	b.WriteString("\tAsserts: []Assertion{\n")
	b.WriteString("\t\t// TODO: pick graders based on the observed suggestion above.\n")
	b.WriteString("\t\t// Candidates: fabrication graders (ContainsTokenNotInContext,\n")
	b.WriteString("\t\t// NamesEntryInDirEntries/NamesPathNotInDirEntries, ContainsURL,\n")
	b.WriteString("\t\t// ContainsHostNotInHistory) if this looks like an invented value;\n")
	b.WriteString("\t\t// syntax graders (ContainsSeparator, StartsWithSeparator,\n")
	b.WriteString("\t\t// RestatesBuffer, ContainsBacktickOrFence) if it looks malformed;\n")
	b.WriteString("\t\t// looping graders (EqualsRecentHistory, InHistory) if it looks like\n")
	b.WriteString("\t\t// a stale repeat.\n")
	b.WriteString("\t},\n")
	b.WriteString("},\n")

	_, err := io.WriteString(w, b.String())
	return err
}

// kindConstant renders a protocol.Request.Kind string value back to its
// symbolic Go constant name (protocol.KindTyping / protocol.KindNextCommand)
// so the emitted stub reads the same way hand-written cases in cases.go do,
// rather than a bare string literal that would silently drift if the
// underlying constant value ever changed. Falls back to a quoted literal for
// any value that doesn't match a known constant (defensive: an event log
// could in principle carry a trigger value from a newer/older protocol
// version than this build knows about).
func kindConstant(kind string) string {
	switch kind {
	case protocol.KindTyping:
		return "protocol.KindTyping"
	case protocol.KindNextCommand:
		return "protocol.KindNextCommand"
	default:
		return fmt.Sprintf("%q", kind)
	}
}

// verifyFormats is a tiny helper the test file uses to double-check a
// rendered stub is syntactically valid Go by round-tripping it through
// go/format. Kept here (not test-only) so cmd/eval's future "render and
// validate" flow can reuse it without duplicating the wrapper shell.
func verifyFormats(src string) error {
	_, err := format.Source([]byte(src))
	return err
}
