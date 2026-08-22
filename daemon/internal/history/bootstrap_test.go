package history

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mustParse fails the test on a scan error so no case can silently assert
// against a truncated parse.
func mustParse(t *testing.T, in string) []Entry {
	t.Helper()
	es, err := parseHistfile(strings.NewReader(in))
	if err != nil {
		t.Fatalf("parseHistfile: %v", err)
	}
	return es
}

func TestParseHistfile_ExtendedHistoryPrefix(t *testing.T) {
	in := ": 1772223958:0;git push\n: 1772223959:2;ls -la\n"
	es := mustParse(t, in)

	eq(t, cmds(es), []string{"git push", "ls -la"})
	if es[0].Ts != 1772223958 || es[1].Ts != 1772223959 {
		t.Fatalf("timestamps = %d,%d", es[0].Ts, es[1].Ts)
	}
	// EXTENDED_HISTORY carries a timestamp but NO directory. Everything
	// bootstrapped is permanently unknown-cwd; that is the whole reason cwd
	// has to be captured going forward.
	for _, e := range es {
		if e.Cwd != "" || e.Session != "" {
			t.Fatalf("bootstrapped entry carries cwd/session: %+v", e)
		}
	}
}

func TestParseHistfile_PlainFormat(t *testing.T) {
	eq(t, cmds(mustParse(t, "git status\nls\n")), []string{"git status", "ls"})
}

// A naive line-per-command split corrupts multiline records.
func TestParseHistfile_MultilineContinuation(t *testing.T) {
	in := ": 1772223958:0;docker run \\\n  -v a:b \\\n  image /bin/bash\n: 1772223999:0;ls\n"
	es := mustParse(t, in)

	if len(es) != 2 {
		t.Fatalf("got %d records %v, want 2 (continuations must not split)", len(es), cmds(es))
	}
	want := "docker run \n  -v a:b \n  image /bin/bash"
	if es[0].Cmd != want {
		t.Fatalf("got %q, want %q", es[0].Cmd, want)
	}
	if es[1].Cmd != "ls" || es[1].Ts != 1772223999 {
		t.Fatalf("record after a continuation is wrong: %+v", es[1])
	}
}

// A command genuinely ending in a backslash is written as two, so "ends in a
// backslash" is the wrong test — it must be an ODD count.
func TestParseHistfile_EvenBackslashesAreNotContinuations(t *testing.T) {
	es := mustParse(t, ": 1:0;echo ends-with-backslash\\\\\n: 2:0;ls\n")
	if len(es) != 2 {
		t.Fatalf("got %d records %v, want 2", len(es), cmds(es))
	}
	if es[0].Cmd != `echo ends-with-backslash\\` {
		t.Fatalf("got %q", es[0].Cmd)
	}
}

func TestOddTrailingBackslashes(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"", false}, {"a", false},
		{`a\`, true}, {`a\\`, false}, {`a\\\`, true}, {`\`, true},
	} {
		if got := oddTrailingBackslashes(tc.in); got != tc.want {
			t.Errorf("%q: got %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestParseHistfile_SkipsBlanksAndFlushesTruncatedTail(t *testing.T) {
	// A file ending mid-continuation still yields the partial command rather
	// than silently dropping the most recent thing the user ran.
	es := mustParse(t, ": 1:0;a\n\n   \n: 2:0;trailing \\\n")
	eq(t, cmds(es), []string{"a", "trailing"})
}

func TestBootstrapFromHistfile_TakesTailAtMax(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "hist")
	var b strings.Builder
	for i := range 10 {
		fmt.Fprintf(&b, ": 1:0;cmd%d\n", i)
	}
	if err := os.WriteFile(p, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	es, err := bootstrapFromHistfile(p, 3)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, cmds(es), []string{"cmd7", "cmd8", "cmd9"})
}

func TestBootstrapFromHistfile_MissingFileIsAnError(t *testing.T) {
	if _, err := bootstrapFromHistfile(filepath.Join(t.TempDir(), "nope"), 10); err == nil {
		t.Fatal("want an error the caller can log and continue past")
	}
}

func TestParseHistfile_ReportsScanError(t *testing.T) {
	// One line past maxRecordBytes stops Scan early. The entries before it are
	// still returned, but the error must surface — silently truncating the
	// bootstrap is the failure this parser exists to avoid.
	huge := strings.Repeat("x", maxRecordBytes+1)
	es, err := parseHistfile(strings.NewReader(": 1:0;keep me\n" + huge + "\n"))
	if err == nil {
		t.Fatal("want an error for an over-long line, got nil")
	}
	eq(t, cmds(es), []string{"keep me"})
}

func TestBootstrapFromHistfile_KeepsPartialParseOnError(t *testing.T) {
	p := filepath.Join(t.TempDir(), "hist")
	huge := strings.Repeat("x", maxRecordBytes+1)
	if err := os.WriteFile(p, []byte(": 1:0;good\n"+huge+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	es, err := bootstrapFromHistfile(p, 100)
	if err == nil {
		t.Fatal("want the scan error propagated")
	}
	eq(t, cmds(es), []string{"good"})
}
