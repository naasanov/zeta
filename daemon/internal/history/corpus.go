package history

import "sync"

// DefaultCorpusMax is how many entries the in-memory corpus holds before it
// halves. ~10k entries is on the order of a megabyte and keeps a full scan
// in the microseconds, which is what every retrieval policy costs.
const DefaultCorpusMax = 10000

// Corpus is the in-memory entry list, held oldest-first as a plain slice (not
// a ring buffer — halving amortizes the copy, and a flat slice scans without
// wrap arithmetic). It performs no I/O and starts no goroutines, so retrieval
// policies are testable against a hand-built one with no setup.
type Corpus struct {
	mu  sync.RWMutex
	max int
	es  []Entry
}

// NewCorpus returns an empty corpus bounded at max entries (<= 0 selects
// DefaultCorpusMax).
func NewCorpus(max int) *Corpus {
	if max <= 0 {
		max = DefaultCorpusMax
	}
	return &Corpus{max: max, es: make([]Entry, 0, min(max, 1024))}
}

// Append adds one entry. This is the ONLY path by which an entry enters the
// corpus — that is deliberate, and it is one of the two places a future
// Indexer would hook (the other is Replay).
func (c *Corpus) Append(e Entry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.es = append(c.es, e)
	c.trimLocked()
}

// Replay discards everything and re-seeds from es, oldest-first — the single
// rebuild path for bootstrap, journal reload, and overflow-halving. That
// keeps frequency counts EXACT: rebuilding from survivors can't drift the
// way an incremental counter would once the older half is dropped.
func (c *Corpus) Replay(es []Entry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.es = append(c.es[:0], es...)
	c.trimLocked()
}

// Snapshot returns a copy of every entry, oldest-first. A copy, not the
// backing array: callers iterate and slice it while other shells append.
func (c *Corpus) Snapshot() []Entry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]Entry, len(c.es))
	copy(out, c.es)
	return out
}

// Len reports how many entries the corpus currently holds.
func (c *Corpus) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.es)
}

// trimLocked halves the corpus once it exceeds max, keeping the newest half.
// Halving rather than dropping one-per-append amortizes the copy to once per
// max/2 commands instead of paying it on every command.
func (c *Corpus) trimLocked() {
	if len(c.es) <= c.max {
		return
	}
	keep := max(c.max/2, 1)
	c.es = append(c.es[:0], c.es[len(c.es)-keep:]...)
}
