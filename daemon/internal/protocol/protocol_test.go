package protocol

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

// TestEncodeDecodeRoundTrip drives a Request through Encode and back through a
// Decoder, exercising the characters that make the wire format tricky: quotes,
// backslashes, embedded newlines/tabs, and the HTML-significant '<>&' that Go
// would otherwise \u-escape.
func TestEncodeDecodeRoundTrip(t *testing.T) {
	cases := []Request{
		{V: Version, ID: "sess.1", Kind: KindTyping, Buf: "git status"},
		{V: Version, ID: "sess.2", Kind: KindTyping, Buf: `echo "hi" > out.txt && cat <in`},
		{V: Version, ID: "sess.3", Kind: KindTyping, Buf: "grep -r 'a\\b' ."},
		{V: Version, ID: "sess.4", Kind: KindTyping, Buf: "line1\nline2\tcol"},
		{V: Version, ID: "sess.5", Kind: KindNextCommand, Buf: ""},
		{
			V: Version, ID: "sess.6", Kind: KindTyping, Buf: "git ad",
			Cwd:        "/Users/x/project",
			GitBranch:  "main",
			GitDirty:   true,
			LastExit:   1,
			History:    []string{"cd project", "npm install", "npm test"},
			DirEntries: []string{"a.txt", "b.txt", "node_modules"},
		},
		{
			V: Version, ID: "sess.7", Kind: KindTyping, Buf: "go bui",
			Cwd:         "/x/gotool",
			History:     []string{"npm install", "cd ../gotool", "go mod tidy"},
			HistoryCwds: []string{"/x/webapp", "/x/webapp", "/x/gotool"},
		},
		{V: Version, ID: "sess.8", Kind: KindRecord, Cmd: "git status", Ts: 1723600000},
	}
	for _, want := range cases {
		var buf bytes.Buffer
		if err := Encode(&buf, want); err != nil {
			t.Fatalf("Encode(%q): %v", want.Buf, err)
		}
		if !strings.HasSuffix(buf.String(), "\n") {
			t.Errorf("encoded message missing trailing newline frame: %q", buf.String())
		}
		var got Request
		if err := NewDecoder(&buf).Decode(&got); err != nil {
			t.Fatalf("Decode(%q): %v", want.Buf, err)
		}
		// Request now carries slice fields (History, DirEntries), so it's no
		// longer comparable with !=; reflect.DeepEqual handles nil-vs-empty
		// and element-wise comparison correctly.
		if !reflect.DeepEqual(got, want) {
			t.Errorf("round trip mismatch:\n got %+v\nwant %+v", got, want)
		}
	}
}

// TestDecodeClientContextJSON decodes JSON in the exact shape the zsh client
// emits. encoding/json ignores unknown keys, so a field-name drift between
// the client and these struct tags leaves a field zero instead of erroring.
func TestDecodeClientContextJSON(t *testing.T) {
	// history_cwds deliberately includes a "" element (the second entry) so
	// the unknown-cwd encoding is pinned, not just the happy path.
	full := `{"v":2,"id":"s.1","kind":"typing","buf":"git sta","cwd":"/home/u/p","git_branch":"phase-1","git_dirty":true,"last_exit":127,"history":["git commit -m \"wip\"","cat a > b & echo hi","ls"],"history_cwds":["/home/u/p","",""],"dir_entries":["a","b"]}`
	var got Request
	if err := NewDecoder(strings.NewReader(full)).Decode(&got); err != nil {
		t.Fatalf("decode full client JSON: %v", err)
	}
	want := Request{
		V: 2, ID: "s.1", Kind: KindTyping, Buf: "git sta",
		Cwd: "/home/u/p", GitBranch: "phase-1", GitDirty: true, LastExit: 127,
		History:     []string{`git commit -m "wip"`, "cat a > b & echo hi", "ls"},
		HistoryCwds: []string{"/home/u/p", "", ""},
		DirEntries:  []string{"a", "b"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("context fields did not land from client JSON:\n got %+v\nwant %+v", got, want)
	}

	// A request with the context fields omitted must decode to zero values.
	min := `{"v":2,"id":"s.2","kind":"next_command","buf":"","cwd":"/tmp"}`
	got = Request{}
	if err := NewDecoder(strings.NewReader(min)).Decode(&got); err != nil {
		t.Fatalf("decode minimal client JSON: %v", err)
	}
	if got.GitBranch != "" || got.GitDirty || got.LastExit != 0 ||
		got.History != nil || got.HistoryCwds != nil || got.DirEntries != nil ||
		got.Cmd != "" || got.Ts != 0 {
		t.Errorf("omitted fields should decode to zero, got %+v", got)
	}

	// KindRecord: the fire-and-forget shape the client sends from preexec.
	record := `{"v":2,"id":"s.3","kind":"record","buf":"","cwd":"/home/u/p","cmd":"git status","ts":1723600000}`
	got = Request{}
	if err := NewDecoder(strings.NewReader(record)).Decode(&got); err != nil {
		t.Fatalf("decode record client JSON: %v", err)
	}
	want = Request{V: 2, ID: "s.3", Kind: KindRecord, Cwd: "/home/u/p", Cmd: "git status", Ts: 1723600000}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("record fields did not land from client JSON:\n got %+v\nwant %+v", got, want)
	}
}

// TestEncodeDisablesHTMLEscaping guards the single most important encoding rule:
// shell metacharacters must appear literally on the wire, because the zsh
// decoder does not handle \uXXXX escapes.
func TestEncodeDisablesHTMLEscaping(t *testing.T) {
	var buf bytes.Buffer
	if err := Encode(&buf, Reply{V: Version, ID: "x", Source: SourceLLM, Suggestion: "cat <a >b &"}); err != nil {
		t.Fatal(err)
	}
	line := buf.String()
	for _, ch := range []string{"<", ">", "&"} {
		if !strings.Contains(line, ch) {
			t.Errorf("expected literal %q in wire output, got: %s", ch, line)
		}
	}
	if strings.Contains(line, `\u003`) {
		t.Errorf("HTML escaping leaked into wire output: %s", line)
	}
}

// TestDecodeStreamFrames confirms the decoder pulls consecutive newline-framed
// messages off a single stream, the way the daemon reads a session.
func TestDecodeStreamFrames(t *testing.T) {
	stream := `{"v":1,"id":"a","kind":"typing","buf":"ls"}` + "\n" +
		`{"v":1,"id":"b","kind":"typing","buf":"cd /"}` + "\n"
	dec := NewDecoder(strings.NewReader(stream))
	ids := []string{}
	for {
		var r Request
		if err := dec.Decode(&r); err != nil {
			break
		}
		ids = append(ids, r.ID)
	}
	if strings.Join(ids, ",") != "a,b" {
		t.Errorf("expected ids a,b; got %v", ids)
	}
}

// TestHistoryWithCwd_Alignment pins the tolerance contract: HistoryCwds may
// be shorter than History, longer, entirely absent, or all-"", and
// HistoryWithCwd must never panic — any unpaired position is just unknown.
func TestHistoryWithCwd_Alignment(t *testing.T) {
	cases := []struct {
		name string
		req  Request
		want []HistoryEntry
	}{
		{
			name: "aligned",
			req:  Request{History: []string{"a", "b"}, HistoryCwds: []string{"/x", "/y"}},
			want: []HistoryEntry{{Cmd: "a", Cwd: "/x"}, {Cmd: "b", Cwd: "/y"}},
		},
		{
			name: "short: HistoryCwds has fewer elements than History",
			req:  Request{History: []string{"a", "b", "c"}, HistoryCwds: []string{"/x"}},
			want: []HistoryEntry{{Cmd: "a", Cwd: "/x"}, {Cmd: "b", Cwd: ""}, {Cmd: "c", Cwd: ""}},
		},
		{
			name: "long: HistoryCwds has more elements than History",
			req:  Request{History: []string{"a"}, HistoryCwds: []string{"/x", "/y", "/z"}},
			want: []HistoryEntry{{Cmd: "a", Cwd: "/x"}},
		},
		{
			name: "absent: HistoryCwds is nil",
			req:  Request{History: []string{"a", "b"}},
			want: []HistoryEntry{{Cmd: "a", Cwd: ""}, {Cmd: "b", Cwd: ""}},
		},
		{
			name: "all-empty: HistoryCwds is present but every element is \"\"",
			req:  Request{History: []string{"a", "b"}, HistoryCwds: []string{"", ""}},
			want: []HistoryEntry{{Cmd: "a", Cwd: ""}, {Cmd: "b", Cwd: ""}},
		},
		{
			name: "no history at all",
			req:  Request{},
			want: []HistoryEntry{},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.req.HistoryWithCwd()
			if len(got) != len(c.want) {
				t.Fatalf("len mismatch: got %+v want %+v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("index %d: got %+v want %+v", i, got[i], c.want[i])
				}
			}
		})
	}
}

// TestHasHistoryCwd covers the passthrough decision inputs: no HistoryCwds,
// an all-"" HistoryCwds, and one with a single known entry.
func TestHasHistoryCwd(t *testing.T) {
	if (Request{History: []string{"a"}}).HasHistoryCwd() {
		t.Error("nil HistoryCwds must report false")
	}
	if (Request{History: []string{"a", "b"}, HistoryCwds: []string{"", ""}}).HasHistoryCwd() {
		t.Error("all-\"\" HistoryCwds must report false")
	}
	if !(Request{History: []string{"a", "b"}, HistoryCwds: []string{"", "/x"}}).HasHistoryCwd() {
		t.Error("a single known cwd must report true")
	}
}

// TestSetHistoryEntries_RoundTrip confirms SetHistoryEntries (the zip) and
// HistoryWithCwd (the unzip) are inverses, and that clearing to an empty
// slice nils both wire fields rather than leaving them empty-but-present.
func TestSetHistoryEntries_RoundTrip(t *testing.T) {
	es := []HistoryEntry{
		{Cmd: "npm install", Cwd: "/x/webapp"},
		{Cmd: "cd ../gotool", Cwd: ""},
		{Cmd: "go build ./...", Cwd: "/x/gotool"},
	}
	var r Request
	r.SetHistoryEntries(es)

	wantHistory := []string{"npm install", "cd ../gotool", "go build ./..."}
	wantCwds := []string{"/x/webapp", "", "/x/gotool"}
	if !reflect.DeepEqual(r.History, wantHistory) {
		t.Errorf("History = %v, want %v", r.History, wantHistory)
	}
	if !reflect.DeepEqual(r.HistoryCwds, wantCwds) {
		t.Errorf("HistoryCwds = %v, want %v", r.HistoryCwds, wantCwds)
	}

	got := r.HistoryWithCwd()
	if !reflect.DeepEqual(got, es) {
		t.Errorf("HistoryWithCwd() = %+v, want %+v", got, es)
	}

	r.SetHistoryEntries(nil)
	if r.History != nil || r.HistoryCwds != nil {
		t.Errorf("SetHistoryEntries(nil) should clear both fields to nil, got History=%v HistoryCwds=%v", r.History, r.HistoryCwds)
	}
}

// TestSetHistory_Segments exercises the segment builders. The "unknown after
// known" subtest is the mirror image of "known then unknown": bootstrapped
// (unknown-cwd) entries followed by tagged ones, in the same mechanism.
func TestSetHistory_Segments(t *testing.T) {
	t.Run("single known segment", func(t *testing.T) {
		var r Request
		r.SetHistory(HistoryIn("/x/gotool", "go mod tidy", "go build ./..."))
		want := Request{
			History:     []string{"go mod tidy", "go build ./..."},
			HistoryCwds: []string{"/x/gotool", "/x/gotool"},
		}
		if !reflect.DeepEqual(r.History, want.History) || !reflect.DeepEqual(r.HistoryCwds, want.HistoryCwds) {
			t.Errorf("got History=%v HistoryCwds=%v, want History=%v HistoryCwds=%v",
				r.History, r.HistoryCwds, want.History, want.HistoryCwds)
		}
	})

	t.Run("all unknown", func(t *testing.T) {
		var r Request
		r.SetHistory(HistoryUnknown("npm install", "npm test"))
		wantCwds := []string{"", ""}
		if !reflect.DeepEqual(r.HistoryCwds, wantCwds) {
			t.Errorf("HistoryCwds = %v, want %v", r.HistoryCwds, wantCwds)
		}
		if r.HasHistoryCwd() {
			t.Error("an all-HistoryUnknown request must report HasHistoryCwd() == false")
		}
	})

	t.Run("known then unknown", func(t *testing.T) {
		var r Request
		r.SetHistory(HistoryIn("/x/gotool", "go mod tidy"), HistoryUnknown("npm install"))
		wantHistory := []string{"go mod tidy", "npm install"}
		wantCwds := []string{"/x/gotool", ""}
		if !reflect.DeepEqual(r.History, wantHistory) || !reflect.DeepEqual(r.HistoryCwds, wantCwds) {
			t.Errorf("got History=%v HistoryCwds=%v, want History=%v HistoryCwds=%v",
				r.History, r.HistoryCwds, wantHistory, wantCwds)
		}
	})

	// "unknown after known": bootstrapped ("" cwd) entries followed by tagged
	// ones. A sticky/inherit-cwd design could not express this (no prior
	// entry to inherit "" from without a second, sentinel mechanism).
	t.Run("unknown after known (E13 shape)", func(t *testing.T) {
		var r Request
		r.SetHistory(
			HistoryUnknown("npm install", "npm run build", "npm test"),
			HistoryIn("/x/gotool", "go mod tidy", "go build ./..."),
		)
		wantHistory := []string{"npm install", "npm run build", "npm test", "go mod tidy", "go build ./..."}
		wantCwds := []string{"", "", "", "/x/gotool", "/x/gotool"}
		if !reflect.DeepEqual(r.History, wantHistory) {
			t.Errorf("History = %v, want %v", r.History, wantHistory)
		}
		if !reflect.DeepEqual(r.HistoryCwds, wantCwds) {
			t.Errorf("HistoryCwds = %v, want %v", r.HistoryCwds, wantCwds)
		}
		if !r.HasHistoryCwd() {
			t.Error("a request with at least one tagged segment must report HasHistoryCwd() == true")
		}
	})

	t.Run("multiple segments interleaving cwds (E14 shape)", func(t *testing.T) {
		var r Request
		r.SetHistory(
			HistoryIn("/home/dir", "echo 1", "cd .."),
			HistoryIn("/home", "echo 2", "cd dir"),
			HistoryIn("/home/dir", "echo 3"),
		)
		wantHistory := []string{"echo 1", "cd ..", "echo 2", "cd dir", "echo 3"}
		wantCwds := []string{"/home/dir", "/home/dir", "/home", "/home", "/home/dir"}
		if !reflect.DeepEqual(r.History, wantHistory) {
			t.Errorf("History = %v, want %v", r.History, wantHistory)
		}
		if !reflect.DeepEqual(r.HistoryCwds, wantCwds) {
			t.Errorf("HistoryCwds = %v, want %v", r.HistoryCwds, wantCwds)
		}
	})

	t.Run("no segments clears fields", func(t *testing.T) {
		r := Request{History: []string{"stale"}, HistoryCwds: []string{"/stale"}}
		r.SetHistory()
		if r.History != nil || r.HistoryCwds != nil {
			t.Errorf("SetHistory() with no segments should clear both fields, got History=%v HistoryCwds=%v", r.History, r.HistoryCwds)
		}
	})
}
