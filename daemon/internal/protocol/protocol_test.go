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

// TestDecodeClientContextJSON decodes JSON in the exact shape the hand-written
// zsh client emits (zsh/50_socket.zsh). It guards the cross-language seam the
// round-trip test can't: encoding/json silently ignores unknown keys, so a
// field-name drift between the client's literal JSON keys and these struct tags
// would NOT fail round-trip — the field would just stay zero. So decode literal
// client bytes and assert the context values actually land.
func TestDecodeClientContextJSON(t *testing.T) {
	full := `{"v":1,"id":"s.1","kind":"typing","buf":"git sta","cwd":"/home/u/p","git_branch":"phase-1","git_dirty":true,"last_exit":127,"history":["git commit -m \"wip\"","cat a > b & echo hi"],"dir_entries":["a","b"]}`
	var got Request
	if err := NewDecoder(strings.NewReader(full)).Decode(&got); err != nil {
		t.Fatalf("decode full client JSON: %v", err)
	}
	want := Request{
		V: 1, ID: "s.1", Kind: KindTyping, Buf: "git sta",
		Cwd: "/home/u/p", GitBranch: "phase-1", GitDirty: true, LastExit: 127,
		History:    []string{`git commit -m "wip"`, "cat a > b & echo hi"},
		DirEntries: []string{"a", "b"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("context fields did not land from client JSON:\n got %+v\nwant %+v", got, want)
	}

	// A request with the context fields omitted must decode to zero values.
	min := `{"v":1,"id":"s.2","kind":"next_command","buf":"","cwd":"/tmp"}`
	got = Request{}
	if err := NewDecoder(strings.NewReader(min)).Decode(&got); err != nil {
		t.Fatalf("decode minimal client JSON: %v", err)
	}
	if got.GitBranch != "" || got.GitDirty || got.LastExit != 0 || got.History != nil || got.DirEntries != nil {
		t.Errorf("omitted fields should decode to zero, got %+v", got)
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
