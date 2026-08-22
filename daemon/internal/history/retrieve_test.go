package history

import "testing"

// fill builds a corpus from alternating cmd/cwd pairs, oldest-first.
func fill(t *testing.T, max int, pairs ...string) *Corpus {
	t.Helper()
	if len(pairs)%2 != 0 {
		t.Fatal("pairs must be cmd,cwd,...")
	}
	c := NewCorpus(max)
	for i := 0; i < len(pairs); i += 2 {
		c.Append(mk(pairs[i], pairs[i+1]))
	}
	return c
}

func TestRecencyOnly_TakesTailAndIgnoresCwd(t *testing.T) {
	c := fill(t, 100, "a", "/x", "b", "/y", "c", "/x", "d", "/y")
	eq(t, cmds(RecencyOnly(c, Query{Cwd: "/x", N: 2})), []string{"c", "d"})
	eq(t, cmds(RecencyOnly(c, Query{Cwd: "/x", N: 99})), []string{"a", "b", "c", "d"})
	eq(t, cmds(RecencyOnly(c, Query{N: 0})), []string{"a", "b", "c", "d"})
}

// The scenario the feature exists for: a day of go work, then a run of npm
// work, then you cd back. Recency alone surfaces zero same-dir candidates.
func TestRecall_SurfacesSameDirBeyondTheRecencyWindow(t *testing.T) {
	c := fill(t, 100,
		"go mod tidy", "/x/gotool",
		"go build ./...", "/x/gotool",
		"npm install", "/x/webapp",
		"npm test", "/x/webapp",
		"npm start", "/x/webapp",
	)
	q := Query{Cwd: "/x/gotool", N: 3}

	eq(t, cmds(RecencyOnly(c, q)), []string{"npm install", "npm test", "npm start"})
	eq(t, cmds(RecencyWithSameDirRecall(c, q)), []string{
		"go mod tidy", "go build ./...", // recalled, older, prepended
		"npm install", "npm test", "npm start",
	})
}

// The load-bearing property of the pool design: a cwd-blind prompt taking
// tail-N of the pool must get exactly what it got before recall existed.
// If recall could displace the tail, every baseline column in every report
// would shift silently.
func TestRecall_NeverDisplacesTheRecencyTail(t *testing.T) {
	c := fill(t, 100,
		"old1", "/dir", "old2", "/dir", "old3", "/dir",
		"r1", "/other", "r2", "/other", "r3", "/other",
	)
	const n = 3
	pool := RecencyWithSameDirRecall(c, Query{Cwd: "/dir", N: n})
	if len(pool) <= n {
		t.Fatalf("expected recall to widen the pool, got %d", len(pool))
	}
	tail := pool[len(pool)-n:]
	eq(t, cmds(tail), cmds(RecencyOnly(c, Query{N: n})))
}

func TestRecall_NoopCases(t *testing.T) {
	c := fill(t, 100, "a", "/x", "b", "/y", "c", "/y")

	t.Run("no cwd on the query", func(t *testing.T) {
		eq(t, cmds(RecencyWithSameDirRecall(c, Query{N: 2})), []string{"b", "c"})
	})
	t.Run("window already covers everything", func(t *testing.T) {
		eq(t, cmds(RecencyWithSameDirRecall(c, Query{Cwd: "/x", N: 99})), []string{"a", "b", "c"})
	})
	t.Run("no same-dir entries anywhere", func(t *testing.T) {
		eq(t, cmds(RecencyWithSameDirRecall(c, Query{Cwd: "/nope", N: 2})), []string{"b", "c"})
	})
	t.Run("empty corpus", func(t *testing.T) {
		if got := RecencyWithSameDirRecall(NewCorpus(10), Query{Cwd: "/x", N: 5}); len(got) != 0 {
			t.Fatalf("got %v, want empty", cmds(got))
		}
	})
}

// Unknown-cwd entries (bootstrapped from $HISTFILE) must never be recalled
// as if they belonged to the current directory.
func TestRecall_IgnoresUnknownCwd(t *testing.T) {
	c := fill(t, 100, "boot1", "", "boot2", "", "r1", "/other", "r2", "/other")
	eq(t, cmds(RecencyWithSameDirRecall(c, Query{Cwd: "", N: 2})), []string{"r1", "r2"})
}

func TestRecall_KeepsTheMostRecentKAndStaysChronological(t *testing.T) {
	c := NewCorpus(1000)
	for i := range DefaultRecallK + 10 {
		c.Append(Entry{Cmd: "old", Cwd: "/dir", Ts: int64(i)})
	}
	for range 5 {
		c.Append(mk("recent", "/other"))
	}
	pool := RecencyWithSameDirRecall(c, Query{Cwd: "/dir", N: 5})

	recalled := pool[:len(pool)-5]
	if len(recalled) != DefaultRecallK {
		t.Fatalf("recalled %d, want cap of %d", len(recalled), DefaultRecallK)
	}
	// Newest K, kept in chronological order.
	if recalled[0].Ts != 10 || recalled[len(recalled)-1].Ts != int64(DefaultRecallK+9) {
		t.Fatalf("recalled span Ts %d..%d, want %d..%d",
			recalled[0].Ts, recalled[len(recalled)-1].Ts, 10, DefaultRecallK+9)
	}
}
