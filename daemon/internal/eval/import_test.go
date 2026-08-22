package eval

import (
	"encoding/json"
	"go/parser"
	"go/token"
	"reflect"
	"strings"
	"testing"

	"github.com/naasanov/zsh-autopilot/daemon/internal/protocol"
)

// requestLineJSON builds a well-formed "request" event JSON line with the
// given buf/suggestion, JSON-escaping them properly (tests below embed
// quotes/special shell syntax that would corrupt hand-built JSON strings).
func requestLineJSON(t *testing.T, buf, suggestion string) string {
	t.Helper()
	ev := struct {
		Event      string `json:"event"`
		RequestID  string `json:"request_id"`
		Trigger    string `json:"trigger"`
		Buf        string `json:"buf"`
		Suggestion string `json:"suggestion"`
	}{
		Event:      "request",
		RequestID:  "s1.1",
		Trigger:    "typing",
		Buf:        buf,
		Suggestion: suggestion,
	}
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return string(b)
}

// realisticRequestLine is a "request" event with raw-text capture on, shaped
// like a real dogfooding row: next-command mode, git context, history, and a
// suggestion that is buf+suffix (buf is empty here, since next-command mode
// always starts from an empty buffer).
const realisticRequestLine = `{"v":1,"event":"request","ts":100.0,"session_id":"s1","request_id":"s1.1","user":"nick","trigger":"next_command","buffer_len":0,"suggestion_len":11,"source":"llm","provider":"codestral","model":"codestral-latest","buf":"","suggestion":"git commit","cwd":"/x/proj","git_branch":"main","git_dirty":true,"history":["git add ."],"dir_entries":["README.md","main.go"]}`

func TestImportEvents_RoundTrip(t *testing.T) {
	cases, stats, err := ImportEvents(strings.NewReader(realisticRequestLine), DefaultImportOptions())
	if err != nil {
		t.Fatalf("ImportEvents: %v", err)
	}
	if len(cases) != 1 {
		t.Fatalf("want 1 case, got %d (stats=%+v)", len(cases), stats)
	}
	ic := cases[0]

	want := protocol.Request{
		Kind:       protocol.KindNextCommand,
		Buf:        "",
		Cwd:        "/x/proj",
		GitBranch:  "main",
		GitDirty:   true,
		History:    []string{"git add ."},
		DirEntries: []string{"README.md", "main.go"},
	}
	// Deep-compare the whole struct rather than field-by-field: the previous
	// form checked only len(DirEntries), so reordered or corrupted entries
	// would have round-tripped "successfully". Reconstruction fidelity is the
	// entire contract of this function — a partial check is worse than none,
	// because it reads as thorough.
	got := ic.Case.Req
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reconstructed request mismatch:\n got %+v\nwant %+v", got, want)
	}
	if ic.Suggestion != "git commit" {
		t.Fatalf("want suffix %q, got %q", "git commit", ic.Suggestion)
	}
	if ic.Provider != "codestral" || ic.Model != "codestral-latest" {
		t.Fatalf("want provider/model codestral/codestral-latest, got %s/%s", ic.Provider, ic.Model)
	}
	if ic.RequestID != "s1.1" {
		t.Fatalf("want request id s1.1, got %s", ic.RequestID)
	}
	if stats.LinesRead != 1 || stats.Imported != 1 {
		t.Fatalf("want LinesRead=1 Imported=1, got %+v", stats)
	}
}

func TestImportEvents_RoundTrip_TypingBufPrefix(t *testing.T) {
	// buf is non-empty here, so the suffix-recovery step (strip the buf
	// prefix from suggestion) is actually exercised, not vacuously true.
	line := `{"event":"request","request_id":"s1.2","trigger":"typing","provider":"anthropic","model":"haiku","buf":"git sta","suggestion":"git status"}`
	cases, stats, err := ImportEvents(strings.NewReader(line), DefaultImportOptions())
	if err != nil {
		t.Fatalf("ImportEvents: %v", err)
	}
	if len(cases) != 1 {
		t.Fatalf("want 1 case, got %d (stats=%+v)", len(cases), stats)
	}
	if cases[0].Suggestion != "tus" {
		t.Fatalf("want suffix %q, got %q", "tus", cases[0].Suggestion)
	}
	if cases[0].Case.Req.Buf != "git sta" {
		t.Fatalf("want buf preserved, got %q", cases[0].Case.Req.Buf)
	}
	if cases[0].Case.Req.Kind != protocol.KindTyping {
		t.Fatalf("want KindTyping, got %q", cases[0].Case.Req.Kind)
	}
}

func TestImportEvents_SkipsNonRequestEvents(t *testing.T) {
	input := strings.Join([]string{
		`{"event":"shown","request_id":"s1.1","total_latency_ms":42}`,
		`{"event":"outcome","request_id":"s1.1","outcome":"accepted"}`,
	}, "\n")
	cases, stats, err := ImportEvents(strings.NewReader(input), DefaultImportOptions())
	if err != nil {
		t.Fatalf("ImportEvents: %v", err)
	}
	if len(cases) != 0 {
		t.Fatalf("want 0 cases, got %d", len(cases))
	}
	if stats.SkippedNotRequest != 2 {
		t.Fatalf("want SkippedNotRequest=2, got %+v", stats)
	}
	if stats.LinesRead != 2 {
		t.Fatalf("want LinesRead=2, got %+v", stats)
	}
}

func TestImportEvents_SkipsNoRawText(t *testing.T) {
	// A request event with raw-text capture OFF: none of the raw-text
	// fields present.
	line := `{"event":"request","request_id":"s1.1","trigger":"typing","provider":"codestral","model":"codestral-latest","buffer_len":3,"suggestion_len":10}`
	cases, stats, err := ImportEvents(strings.NewReader(line), DefaultImportOptions())
	if err != nil {
		t.Fatalf("ImportEvents: %v", err)
	}
	if len(cases) != 0 {
		t.Fatalf("want 0 cases, got %d", len(cases))
	}
	if stats.SkippedNoRawText != 1 {
		t.Fatalf("want SkippedNoRawText=1, got %+v", stats)
	}
}

func TestImportEvents_MalformedSuggestionPrefixCountedNotMangled(t *testing.T) {
	// suggestion does NOT start with buf -- malformed row.
	line := `{"event":"request","request_id":"s1.1","trigger":"typing","buf":"git status","suggestion":"totally unrelated"}`
	cases, stats, err := ImportEvents(strings.NewReader(line), DefaultImportOptions())
	if err != nil {
		t.Fatalf("ImportEvents: %v", err)
	}
	if len(cases) != 0 {
		t.Fatalf("want 0 cases (malformed row must not be silently mangled into a case), got %d", len(cases))
	}
	if stats.SkippedBadPrefix != 1 {
		t.Fatalf("want SkippedBadPrefix=1, got %+v", stats)
	}
}

func TestImportEvents_MalformedJSONDoesNotAbortImport(t *testing.T) {
	// A truncated/corrupt line in the middle must not stop later valid
	// lines from importing -- an append-only log can have a torn last line.
	good1 := `{"event":"request","request_id":"s1.1","trigger":"typing","buf":"a","suggestion":"ab"}`
	bad := `{"event":"request", not valid json`
	good2 := `{"event":"request","request_id":"s1.2","trigger":"typing","buf":"c","suggestion":"cd"}`
	input := strings.Join([]string{good1, bad, good2}, "\n")

	cases, stats, err := ImportEvents(strings.NewReader(input), DefaultImportOptions())
	if err != nil {
		t.Fatalf("ImportEvents: %v", err)
	}
	if len(cases) != 2 {
		t.Fatalf("want 2 cases despite one malformed line, got %d (stats=%+v)", len(cases), stats)
	}
	if stats.SkippedMalformedJSON != 1 {
		t.Fatalf("want SkippedMalformedJSON=1, got %+v", stats)
	}
	if stats.LinesRead != 3 {
		t.Fatalf("want LinesRead=3, got %+v", stats)
	}
}

func TestImportEvents_TriggerFilter(t *testing.T) {
	input := strings.Join([]string{
		`{"event":"request","request_id":"s1.1","trigger":"typing","buf":"a","suggestion":"ab"}`,
		`{"event":"request","request_id":"s1.2","trigger":"next_command","buf":"","suggestion":"ls"}`,
	}, "\n")
	cases, stats, err := ImportEvents(strings.NewReader(input), ImportOptions{TriggerFilter: protocol.KindNextCommand, SkipLikelySecrets: true})
	if err != nil {
		t.Fatalf("ImportEvents: %v", err)
	}
	if len(cases) != 1 || cases[0].Case.Req.Kind != protocol.KindNextCommand {
		t.Fatalf("want exactly the next_command row, got %+v", cases)
	}
	if stats.SkippedTriggerFilter != 1 {
		t.Fatalf("want SkippedTriggerFilter=1, got %+v", stats)
	}
}

func TestImportEvents_Dedup(t *testing.T) {
	line := `{"event":"request","request_id":"s1.1","trigger":"typing","buf":"git sta","suggestion":"git status"}`
	line2 := `{"event":"request","request_id":"s1.99","trigger":"typing","buf":"git sta","suggestion":"git status"}`
	input := strings.Join([]string{line, line, line2}, "\n")

	cases, stats, err := ImportEvents(strings.NewReader(input), DefaultImportOptions())
	if err != nil {
		t.Fatalf("ImportEvents: %v", err)
	}
	if len(cases) != 1 {
		t.Fatalf("want 1 case after dedup, got %d", len(cases))
	}
	if stats.Duplicates != 2 {
		t.Fatalf("want Duplicates=2, got %+v", stats)
	}
}

func TestImportEvents_DedupDistinguishesDifferentSuggestions(t *testing.T) {
	// Same request, different observed output -- must NOT collapse, since
	// the whole point is capturing what the model actually said.
	input := strings.Join([]string{
		`{"event":"request","request_id":"s1.1","trigger":"typing","buf":"git sta","suggestion":"git status"}`,
		`{"event":"request","request_id":"s1.2","trigger":"typing","buf":"git sta","suggestion":"git stash"}`,
	}, "\n")
	cases, stats, err := ImportEvents(strings.NewReader(input), DefaultImportOptions())
	if err != nil {
		t.Fatalf("ImportEvents: %v", err)
	}
	if len(cases) != 2 {
		t.Fatalf("want 2 distinct cases, got %d (stats=%+v)", len(cases), stats)
	}
}

func TestImportEvents_MaxCases(t *testing.T) {
	input := strings.Join([]string{
		`{"event":"request","request_id":"s1.1","trigger":"typing","buf":"a","suggestion":"a1"}`,
		`{"event":"request","request_id":"s1.2","trigger":"typing","buf":"b","suggestion":"b2"}`,
		`{"event":"request","request_id":"s1.3","trigger":"typing","buf":"c","suggestion":"c3"}`,
	}, "\n")
	opts := DefaultImportOptions()
	opts.MaxCases = 2
	cases, stats, err := ImportEvents(strings.NewReader(input), opts)
	if err != nil {
		t.Fatalf("ImportEvents: %v", err)
	}
	if len(cases) != 2 {
		t.Fatalf("want 2 cases (capped), got %d", len(cases))
	}
	if stats.SkippedOverCap != 1 {
		t.Fatalf("want SkippedOverCap=1, got %+v", stats)
	}
}

// ---- Secret heuristics ------------------------------------------------

func TestImportEvents_SecretHeuristics_CaughtAndSkipped(t *testing.T) {
	tests := map[string]string{
		"aws_access_key":  `AKIAABCDEFGHIJKLMNOP`,
		"openai_style":    `sk-abcdefghijklmnopqrstuvwx`,
		"github_pat":      `ghp_abcdefghijklmnopqrstuvwxyz1234`,
		"pem_block":       `-----BEGIN RSA PRIVATE KEY-----`,
		"password_assign": `password=hunter2ishuntertwo`,
		"api_key_assign":  `api_key=abcd1234efgh5678`,
		"long_hex":        strings.Repeat("a1b2c3d4", 5),         // 40 hex chars
		"long_base64":     strings.Repeat("QUJDREVGR0gxMjM0", 3), // 48 base64-alphabet chars, no embedded padding
	}
	for name, secret := range tests {
		t.Run(name, func(t *testing.T) {
			line := requestLineJSON(t, "echo "+secret, "echo "+secret+" done")
			cases, stats, err := ImportEvents(strings.NewReader(line), DefaultImportOptions())
			if err != nil {
				t.Fatalf("ImportEvents: %v", err)
			}
			if len(cases) != 0 {
				t.Fatalf("%s: want secret row skipped, got %d cases", name, len(cases))
			}
			if stats.SkippedLikelySecret != 1 {
				t.Fatalf("%s: want SkippedLikelySecret=1, got %+v", name, stats)
			}
		})
	}
}

func TestImportEvents_SecretHeuristics_BenignCommandNotCaught(t *testing.T) {
	benign := []string{
		`git commit -m "fix bug in parser"`,
		`ls -la /usr/local/bin`,
		`go test ./... -run TestFoo -v`,
		`curl https://example.com/api/v1/status`,
		`docker compose up -d --build`,
	}
	for _, cmd := range benign {
		t.Run(cmd, func(t *testing.T) {
			line := requestLineJSON(t, cmd, cmd+" --verbose")
			cases, stats, err := ImportEvents(strings.NewReader(line), DefaultImportOptions())
			if err != nil {
				t.Fatalf("ImportEvents: %v", err)
			}
			if len(cases) != 1 {
				t.Fatalf("benign command %q wrongly flagged as secret (stats=%+v)", cmd, stats)
			}
		})
	}
}

func TestImportEvents_SecretSkip_CanBeDisabled(t *testing.T) {
	line := `{"event":"request","request_id":"s1.1","trigger":"typing","buf":"echo AKIAABCDEFGHIJKLMNOP","suggestion":"echo AKIAABCDEFGHIJKLMNOP done"}`
	opts := ImportOptions{SkipLikelySecrets: false}
	cases, stats, err := ImportEvents(strings.NewReader(line), opts)
	if err != nil {
		t.Fatalf("ImportEvents: %v", err)
	}
	if len(cases) != 1 {
		t.Fatalf("want secret row imported when SkipLikelySecrets is explicitly off, got %d cases (stats=%+v)", len(cases), stats)
	}
}

// ---- historySegments ------------------------------------------------------

func TestHistorySegments_MixedSequence(t *testing.T) {
	// unknown, then dir A, then dir B, then unknown again -- must produce
	// four segments in order, not collapse or reorder them.
	req := protocol.Request{
		History:     []string{"ls", "go build", "go test", "npm start", "echo done"},
		HistoryCwds: []string{"", "/x/gotool", "/x/gotool", "/x/webapp", ""},
	}
	segs := historySegments(req)
	want := []protocol.HistorySegment{
		{Cwd: "", Cmds: []string{"ls"}},
		{Cwd: "/x/gotool", Cmds: []string{"go build", "go test"}},
		{Cwd: "/x/webapp", Cmds: []string{"npm start"}},
		{Cwd: "", Cmds: []string{"echo done"}},
	}
	if !reflect.DeepEqual(segs, want) {
		t.Fatalf("segments mismatch:\n got %+v\nwant %+v", segs, want)
	}
}

func TestHistorySegments_HistoryCwdsAbsent(t *testing.T) {
	req := protocol.Request{History: []string{"git add .", "git status"}}
	segs := historySegments(req)
	want := []protocol.HistorySegment{{Cwd: "", Cmds: []string{"git add .", "git status"}}}
	if !reflect.DeepEqual(segs, want) {
		t.Fatalf("segments mismatch:\n got %+v\nwant %+v", segs, want)
	}
}

func TestHistorySegments_HistoryCwdsShorterThanHistory(t *testing.T) {
	// HistoryWithCwd treats any unpaired tail position as unknown ("").
	req := protocol.Request{
		History:     []string{"go build", "go test", "npm start"},
		HistoryCwds: []string{"/x/gotool"},
	}
	segs := historySegments(req)
	want := []protocol.HistorySegment{
		{Cwd: "/x/gotool", Cmds: []string{"go build"}},
		{Cwd: "", Cmds: []string{"go test", "npm start"}},
	}
	if !reflect.DeepEqual(segs, want) {
		t.Fatalf("segments mismatch:\n got %+v\nwant %+v", segs, want)
	}
}

func TestHistorySegments_AllEmpty(t *testing.T) {
	if segs := historySegments(protocol.Request{}); len(segs) != 0 {
		t.Fatalf("want no segments for empty history, got %+v", segs)
	}
}

// ---- RenderCaseStub -----------------------------------------------------

// wrapAsFile wraps a rendered stub fragment in a minimal but complete Go
// source file so it can be checked with go/parser: RenderCaseStub emits a
// single composite-literal statement meant to be pasted inside a []Case{...}
// slice, not a standalone file.
func wrapAsFile(fragment string) string {
	return "package p\n\nimport \"github.com/naasanov/zsh-autopilot/daemon/internal/protocol\"\n\nvar cases = []Case{\n" + fragment + "\n}\n"
}

func TestRenderCaseStub_ProducesValidGo(t *testing.T) {
	ic := ImportedCase{
		Case: Case{
			Req: protocol.Request{
				Kind:       protocol.KindTyping,
				Buf:        "git sta",
				Cwd:        "/x/proj",
				GitBranch:  "main",
				GitDirty:   true,
				LastExit:   1,
				History:    []string{"git add .", "git status"},
				DirEntries: []string{"README.md", "main.go"},
			},
		},
		Suggestion: "tus",
		Provider:   "codestral",
		Model:      "codestral-latest",
		RequestID:  "s1.42",
	}

	var b strings.Builder
	if err := RenderCaseStub(&b, ic, "IMPORT-1"); err != nil {
		t.Fatalf("RenderCaseStub: %v", err)
	}

	src := wrapAsFile(b.String())
	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, "stub.go", src, parser.AllErrors); err != nil {
		t.Fatalf("rendered stub is not valid Go: %v\n---\n%s", err, src)
	}
	if err := verifyFormats(src); err != nil {
		t.Fatalf("rendered stub does not gofmt cleanly: %v\n---\n%s", err, src)
	}

	out := b.String()
	for _, want := range []string{
		`ID:       "IMPORT-1"`,
		`func() protocol.Request {`,
		`r := protocol.Request{`,
		`Kind: protocol.KindTyping`,
		`Buf: "git sta"`,
		`Cwd: "/x/proj"`,
		`GitBranch: "main"`,
		`GitDirty: true`,
		`LastExit: 1`,
		`r.SetHistory(`,
		`protocol.HistoryUnknown("git add .", "git status")`,
		`return r`,
		`}(),`,
		`"README.md"`,
		`"main.go"`,
		`s1.42`,
		`codestral`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered stub missing %q in:\n%s", want, out)
		}
	}
	// History is rendered via SetHistory, not as a raw array literal.
	if strings.Contains(out, "History: []string{") {
		t.Fatalf("rendered stub still uses raw History array literal:\n%s", out)
	}
}

func TestRenderCaseStub_HistoryWithCwds_EmitsHistoryInSegments(t *testing.T) {
	req := protocol.Request{Kind: protocol.KindNextCommand, Cwd: "/x/gotool"}
	req.SetHistory(
		protocol.HistoryUnknown("npm install"),
		protocol.HistoryIn("/x/gotool", "go mod tidy", "go build ./..."),
		protocol.HistoryIn("/x/webapp", "npm test"),
		protocol.HistoryUnknown("echo done"),
	)
	ic := ImportedCase{
		Case:       Case{Req: req},
		Suggestion: "git status",
		Provider:   "codestral",
		Model:      "codestral-latest",
		RequestID:  "s1.9",
	}

	var b strings.Builder
	if err := RenderCaseStub(&b, ic, "IMPORT-4"); err != nil {
		t.Fatalf("RenderCaseStub: %v", err)
	}

	src := wrapAsFile(b.String())
	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, "stub.go", src, parser.AllErrors); err != nil {
		t.Fatalf("rendered stub is not valid Go: %v\n---\n%s", err, src)
	}
	if err := verifyFormats(src); err != nil {
		t.Fatalf("rendered stub does not gofmt cleanly: %v\n---\n%s", err, src)
	}

	out := b.String()
	// Segments must appear in order, each grouped by contiguous cwd.
	idxUnknown1 := strings.Index(out, `protocol.HistoryUnknown("npm install")`)
	idxGotool := strings.Index(out, `protocol.HistoryIn("/x/gotool", "go mod tidy", "go build ./...")`)
	idxWebapp := strings.Index(out, `protocol.HistoryIn("/x/webapp", "npm test")`)
	idxUnknown2 := strings.Index(out, `protocol.HistoryUnknown("echo done")`)
	if idxUnknown1 < 0 || idxGotool < 0 || idxWebapp < 0 || idxUnknown2 < 0 {
		t.Fatalf("rendered stub missing expected segment calls:\n%s", out)
	}
	if !(idxUnknown1 < idxGotool && idxGotool < idxWebapp && idxWebapp < idxUnknown2) {
		t.Fatalf("rendered segments out of order:\n%s", out)
	}
}

func TestRenderCaseStub_HandlesUnicodeAndEmbeddedQuotesNewlines(t *testing.T) {
	ic := ImportedCase{
		Case: Case{
			Req: protocol.Request{
				Kind: protocol.KindTyping,
				Buf:  "echo \"héllo\nwörld\" 'quoted'",
			},
		},
		Suggestion: "done",
		Provider:   "codestral",
		Model:      "codestral-latest",
		RequestID:  "s1.7",
	}

	var b strings.Builder
	if err := RenderCaseStub(&b, ic, "IMPORT-2"); err != nil {
		t.Fatalf("RenderCaseStub: %v", err)
	}

	src := wrapAsFile(b.String())
	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, "stub.go", src, parser.AllErrors); err != nil {
		t.Fatalf("rendered stub with unicode/quotes/newlines is not valid Go: %v\n---\n%s", err, src)
	}
}

func TestRenderCaseStub_KindNextCommand(t *testing.T) {
	ic := ImportedCase{
		Case: Case{
			Req: protocol.Request{Kind: protocol.KindNextCommand},
		},
		Suggestion: "git status",
	}
	var b strings.Builder
	if err := RenderCaseStub(&b, ic, "IMPORT-3"); err != nil {
		t.Fatalf("RenderCaseStub: %v", err)
	}
	if !strings.Contains(b.String(), "protocol.KindNextCommand") {
		t.Fatalf("want symbolic KindNextCommand constant in output, got:\n%s", b.String())
	}
}
