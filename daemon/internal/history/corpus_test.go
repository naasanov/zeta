package history

import "testing"

func mk(cmd, cwd string) Entry { return Entry{Cmd: cmd, Cwd: cwd} }

func cmds(es []Entry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.Cmd
	}
	return out
}

func eq(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("len = %d %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestCorpus_AppendKeepsOldestFirst(t *testing.T) {
	c := NewCorpus(10)
	for _, s := range []string{"a", "b", "c"} {
		c.Append(mk(s, "/x"))
	}
	eq(t, cmds(c.Snapshot()), []string{"a", "b", "c"})
}

func TestCorpus_HalvesOnOverflow(t *testing.T) {
	c := NewCorpus(4)
	for _, s := range []string{"a", "b", "c", "d", "e"} {
		c.Append(mk(s, "/x"))
	}
	eq(t, cmds(c.Snapshot()), []string{"d", "e"})
	if c.Len() != 2 {
		t.Fatalf("Len = %d, want 2", c.Len())
	}
}

func TestCorpus_ReplayResetsAndTrims(t *testing.T) {
	c := NewCorpus(4)
	c.Append(mk("old", "/x"))

	c.Replay([]Entry{mk("a", "/x"), mk("b", "/x"), mk("c", "/x")})
	eq(t, cmds(c.Snapshot()), []string{"a", "b", "c"})

	c.Replay([]Entry{mk("a", ""), mk("b", ""), mk("c", ""), mk("d", ""), mk("e", "")})
	eq(t, cmds(c.Snapshot()), []string{"d", "e"})
}

func TestCorpus_SnapshotIsACopy(t *testing.T) {
	c := NewCorpus(10)
	c.Append(mk("a", "/x"))
	s := c.Snapshot()
	s[0].Cmd = "mutated"
	if c.Snapshot()[0].Cmd != "a" {
		t.Fatal("Snapshot handed out the backing array; callers can corrupt the corpus")
	}
}

func TestCorpus_ZeroMaxUsesDefault(t *testing.T) {
	if got := NewCorpus(0).max; got != DefaultCorpusMax {
		t.Fatalf("max = %d, want %d", got, DefaultCorpusMax)
	}
}

func TestCorpus_ConcurrentAppendAndSnapshot(t *testing.T) {
	c := NewCorpus(1000)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 500 {
			c.Append(mk("x", "/x"))
		}
	}()
	for range 500 {
		_ = c.Snapshot()
	}
	<-done
}
