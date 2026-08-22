// Importer that turns real dogfooding failures (the §12 metrics log) into
// eval Cases: reads events.jsonl, reconstructs the protocol.Request behind
// each "request" event, and recovers the completion suffix the model emitted.
//
// rawRequestEvent below is a local, minimal mirror of metrics.RequestEvent's
// JSON shape rather than an import of internal/metrics — metrics must stay
// deletable as one directory (see CLAUDE.md), so this package depends on the
// wire format, not the Go type.
//
// SkipLikelySecrets is a best-effort heuristic filter, NOT redaction — it
// will miss secrets with no distinguishing shape. A human MUST review every
// rendered stub before pasting it into cases.go.
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
	Buf         string   `json:"buf,omitempty"`
	Suggestion  string   `json:"suggestion,omitempty"`
	Cwd         string   `json:"cwd,omitempty"`
	GitBranch   string   `json:"git_branch,omitempty"`
	GitDirty    bool     `json:"git_dirty,omitempty"`
	LastExit    int      `json:"last_exit,omitempty"`
	History     []string `json:"history,omitempty"`
	HistoryCwds []string `json:"history_cwds,omitempty"` // index-aligned with History; "" = unknown dir
	DirEntries  []string `json:"dir_entries,omitempty"`
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
	// secret shapes. Use DefaultImportOptions for the safe (skipped) default
	// — a bare struct literal defaults this to false, Go having no "unset"
	// state for a bool field.
	SkipLikelySecrets bool

	secretsExplicitlySet bool
}

// DefaultImportOptions returns the safe default: no trigger filter, no cap,
// secrets skipped.
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
// every row that did NOT make it in. Callers wanting the safe secret-skipping
// default should build opts via DefaultImportOptions.
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
			len(ev.History) > 0 || len(ev.HistoryCwds) > 0 || len(ev.DirEntries) > 0
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
			Kind:        ev.Trigger,
			Buf:         ev.Buf,
			Cwd:         ev.Cwd,
			GitBranch:   ev.GitBranch,
			GitDirty:    ev.GitDirty,
			LastExit:    ev.LastExit,
			History:     ev.History,
			HistoryCwds: ev.HistoryCwds,
			DirEntries:  ev.DirEntries,
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
// request_id produced them.
func dedupKey(req protocol.Request, suggestion string) string {
	b, _ := json.Marshal(req) // Request is plain data; Marshal cannot fail here
	h := sha256.New()
	h.Write(b)
	h.Write([]byte{0})
	h.Write([]byte(suggestion))
	return hex.EncodeToString(h.Sum(nil))
}

// historySegments groups req's aligned History/HistoryCwds entries (via
// HistoryWithCwd, which already tolerates absent/short HistoryCwds) into
// runs sharing one cwd, in order -- the shape SetHistory's segments expect.
func historySegments(req protocol.Request) []protocol.HistorySegment {
	var segs []protocol.HistorySegment
	for _, e := range req.HistoryWithCwd() {
		if n := len(segs); n > 0 && segs[n-1].Cwd == e.Cwd {
			segs[n-1].Cmds = append(segs[n-1].Cmds, e.Cmd)
			continue
		}
		segs = append(segs, protocol.HistorySegment{Cwd: e.Cwd, Cmds: []string{e.Cmd}})
	}
	return segs
}

// writeReqFields renders req's scalar and DirEntries fields at indent.
// History/HistoryCwds are excluded here -- see writeHistorySegments, which
// renders them as protocol.HistoryIn/HistoryUnknown calls instead.
func writeReqFields(b *strings.Builder, req protocol.Request, indent string) {
	if req.Kind != "" {
		fmt.Fprintf(b, "%sKind: %s,\n", indent, kindConstant(req.Kind))
	}
	if req.Buf != "" {
		fmt.Fprintf(b, "%sBuf: %q,\n", indent, req.Buf)
	}
	if req.Cwd != "" {
		fmt.Fprintf(b, "%sCwd: %q,\n", indent, req.Cwd)
	}
	if req.GitBranch != "" {
		fmt.Fprintf(b, "%sGitBranch: %q,\n", indent, req.GitBranch)
	}
	if req.GitDirty {
		fmt.Fprintf(b, "%sGitDirty: true,\n", indent)
	}
	if req.LastExit != 0 {
		fmt.Fprintf(b, "%sLastExit: %d,\n", indent, req.LastExit)
	}
	if len(req.DirEntries) > 0 {
		fmt.Fprintf(b, "%sDirEntries: []string{\n", indent)
		for _, d := range req.DirEntries {
			fmt.Fprintf(b, "%s\t%q,\n", indent, d)
		}
		fmt.Fprintf(b, "%s},\n", indent)
	}
}

// writeHistorySegments renders segs as chained protocol.HistoryIn/
// HistoryUnknown calls feeding r.SetHistory(...), matching the hand-written
// E9-E14 shape in cases.go rather than raw parallel array literals.
func writeHistorySegments(b *strings.Builder, segs []protocol.HistorySegment, indent string) {
	fmt.Fprintf(b, "%sr.SetHistory(\n", indent)
	for _, seg := range segs {
		fn := "protocol.HistoryIn"
		var args []string
		if seg.Cwd == "" {
			fn = "protocol.HistoryUnknown"
		} else {
			args = append(args, fmt.Sprintf("%q", seg.Cwd))
		}
		for _, c := range seg.Cmds {
			args = append(args, fmt.Sprintf("%q", c))
		}
		fmt.Fprintf(b, "%s\t%s(%s),\n", indent, fn, strings.Join(args, ", "))
	}
	fmt.Fprintf(b, "%s)\n", indent)
}

// RenderCaseStub emits one Go composite-literal Case entry (not a full
// source file) meant to be pasted into a []Case{...} slice in cases.go:
// Req (an IIFE when history is present, since SetHistory is a method),
// the observed suggestion in a comment, and a TODO for the assertions.
func RenderCaseStub(w io.Writer, ic ImportedCase, id string) error {
	var b strings.Builder

	fmt.Fprintf(&b, "// Imported from dogfooding (request_id=%s, provider=%s, model=%s).\n", ic.RequestID, ic.Provider, ic.Model)
	fmt.Fprintf(&b, "// Observed suggestion (the model's completion SUFFIX): %q\n", ic.Suggestion)
	b.WriteString("// A human must review this stub for secrets before committing it —\n")
	b.WriteString("// SkipLikelySecrets is a heuristic filter, not redaction.\n")
	fmt.Fprintf(&b, "{\n\tID:       %q,\n", id)
	b.WriteString("\tCategory: \"TODO\",\n")

	req := ic.Case.Req
	if segs := historySegments(req); len(segs) > 0 {
		b.WriteString("\tReq: func() protocol.Request {\n")
		b.WriteString("\t\tr := protocol.Request{\n")
		writeReqFields(&b, req, "\t\t\t")
		b.WriteString("\t\t}\n")
		writeHistorySegments(&b, segs, "\t\t")
		b.WriteString("\t\treturn r\n")
		b.WriteString("\t}(),\n")
	} else {
		b.WriteString("\tReq: protocol.Request{\n")
		writeReqFields(&b, req, "\t\t")
		b.WriteString("\t},\n")
	}

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

// kindConstant renders a Kind string back to its symbolic constant name so
// the stub matches hand-written cases.go entries, falling back to a quoted
// literal for an unrecognized value.
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
