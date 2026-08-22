package history

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func quiet() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newStore(t *testing.T, cfg Config) *Store {
	t.Helper()
	cfg.Log = quiet()
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestJournal_WriteReadRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "history.jsonl")
	j, err := openJournal(p, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []Entry{
		{Cmd: `echo "a > b & c < d"`, Cwd: "/x", Session: "s1", Ts: 1},
		{Cmd: "multi\nline", Cwd: "/y", Session: "s1", Ts: 2},
	}
	for _, e := range want {
		j.write(e)
	}
	if err := j.close(); err != nil {
		t.Fatal(err)
	}

	got, err := readJournal(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("entry %d: got %+v, want %+v", i, got[i], want[i])
		}
	}

	// Same reason protocol.Encode disables it: shell text is full of these
	// and \uXXXX would be unreadable in the journal and lossy to re-read.
	raw, _ := os.ReadFile(p)
	if strings.Contains(string(raw), `\u003e`) || !strings.Contains(string(raw), `>`) {
		t.Fatalf("journal HTML-escaped '>'; escaping must be off. got:\n%s", raw)
	}
}

func TestJournal_Mode0600(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "history.jsonl")
	j, err := openJournal(p, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	j.close()

	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	// Unredacted command lines: not world-readable.
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestJournal_RotatesToNewestHalf(t *testing.T) {
	p := filepath.Join(t.TempDir(), "history.jsonl")
	j, err := openJournal(p, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 12 {
		j.write(Entry{Cmd: "c", Ts: int64(i)})
	}
	if err := j.close(); err != nil {
		t.Fatal(err)
	}

	got, err := readJournal(p)
	if err != nil {
		t.Fatal(err)
	}
	// Rotation fires at max+1 and keeps max/2; writes after it append again,
	// so the bound is "stays under max", not an exact count.
	if len(got) == 12 || len(got) > 10 {
		t.Fatalf("got %d records, want the journal rotated below max=10", len(got))
	}
	// Whatever survived must be the NEWEST records, contiguous to the last.
	if got[len(got)-1].Ts != 11 {
		t.Fatalf("newest kept Ts = %d, want 11", got[len(got)-1].Ts)
	}
	for i := 1; i < len(got); i++ {
		if got[i].Ts != got[i-1].Ts+1 {
			t.Fatalf("kept records are not contiguous: %d then %d", got[i-1].Ts, got[i].Ts)
		}
	}
}

func TestJournal_WriteAfterCloseIsSafe(t *testing.T) {
	p := filepath.Join(t.TempDir(), "history.jsonl")
	j, _ := openJournal(p, 100, 0)
	j.close()
	j.write(Entry{Cmd: "after close"}) // must not panic on a closed channel
	if err := j.close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func TestStore_BootstrapsOnceThenNeverAgain(t *testing.T) {
	dir := t.TempDir()
	hist := filepath.Join(dir, "zsh_history")
	jrnl := filepath.Join(dir, "history.jsonl")
	if err := os.WriteFile(hist, []byte(": 1:0;boot a\n: 2:0;boot b\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := Config{JournalPath: jrnl, HistfilePath: hist, CorpusMax: 100, JournalMax: 100}

	s1 := newStore(t, cfg)
	eq(t, cmds(s1.Select(Query{N: 10})), []string{"boot a", "boot b"})
	s1.Record(Entry{Cmd: "live one", Cwd: "/x", Session: "s1", Ts: 3})
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}

	// The journal now exists, so $HISTFILE must not be read again — otherwise
	// every restart would duplicate the whole bootstrap corpus.
	s2 := newStore(t, cfg)
	eq(t, cmds(s2.Select(Query{N: 10})), []string{"boot a", "boot b", "live one"})
}

func TestStore_JournalAbsenceIsTheWatermark(t *testing.T) {
	dir := t.TempDir()
	hist := filepath.Join(dir, "zsh_history")
	if err := os.WriteFile(hist, []byte(": 1:0;from histfile\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	jrnl := filepath.Join(dir, "history.jsonl")

	// A pre-existing journal, even an empty one, means "already bootstrapped".
	if err := os.WriteFile(jrnl, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s := newStore(t, Config{JournalPath: jrnl, HistfilePath: hist, CorpusMax: 100})
	if got := s.Select(Query{N: 10}); len(got) != 0 {
		t.Fatalf("got %v, want empty — an existing journal must suppress bootstrap", cmds(got))
	}
}

func TestStore_MissingHistfileIsNotFatal(t *testing.T) {
	dir := t.TempDir()
	s := newStore(t, Config{
		JournalPath:  filepath.Join(dir, "history.jsonl"),
		HistfilePath: filepath.Join(dir, "does-not-exist"),
		CorpusMax:    100,
	})
	if s.Len() != 0 {
		t.Fatalf("Len = %d, want 0", s.Len())
	}
	s.Record(Entry{Cmd: "still works", Cwd: "/x"})
	eq(t, cmds(s.Select(Query{N: 10})), []string{"still works"})
}

func TestStore_SelectUsesTheRetrieveField(t *testing.T) {
	s := newStore(t, Config{JournalPath: filepath.Join(t.TempDir(), "h.jsonl"), CorpusMax: 100})
	s.Record(Entry{Cmd: "a", Cwd: "/x"})

	called := false
	s.Retrieve = func(c *Corpus, q Query) []Entry {
		called = true
		return nil
	}
	s.Select(Query{N: 1})
	if !called {
		t.Fatal("Select ignored the Retrieve field; swapping policy in one line is the point")
	}
}

func TestStore_RecordIgnoresEmptyCommands(t *testing.T) {
	s := newStore(t, Config{JournalPath: filepath.Join(t.TempDir(), "h.jsonl"), CorpusMax: 100})
	s.Record(Entry{Cmd: "", Cwd: "/x"})
	if s.Len() != 0 {
		t.Fatalf("Len = %d, want 0", s.Len())
	}
}

func TestStore_NilIsSafe(t *testing.T) {
	var s *Store
	s.Record(Entry{Cmd: "x"})
	if s.Select(Query{N: 1}) != nil || s.Len() != 0 || s.Close() != nil {
		t.Fatal("nil Store must be a safe no-op")
	}
}
