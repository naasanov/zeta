package protocol

import (
	"bytes"
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
		if got != want {
			t.Errorf("round trip mismatch:\n got %+v\nwant %+v", got, want)
		}
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
