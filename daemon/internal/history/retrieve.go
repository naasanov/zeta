package history

import "slices"

// DefaultRecallK bounds how many older same-directory entries a recall
// policy may pull in behind the recency window.
const DefaultRecallK = 20

// DefaultPoolN is the recency depth of the candidate pool, deliberately well
// above any prompt's render count: retrieval returns a pool and each prompt
// truncates it itself.
const DefaultPoolN = 100

// Retriever selects the candidate pool for one request; a plain function so
// a policy can be swapped in the composition root and tested standalone.
type Retriever func(*Corpus, Query) []Entry

// RecencyOnly returns the last q.N entries, oldest-first, ignoring q.Cwd
// entirely.
func RecencyOnly(c *Corpus, q Query) []Entry {
	es := c.Snapshot()
	if q.N > 0 && len(es) > q.N {
		es = es[len(es)-q.N:]
	}
	return es
}

// RecencyWithSameDirRecall returns the last q.N entries plus up to
// DefaultRecallK older ones from q.Cwd. Recalled entries all predate the
// window, so they never displace the tail.
func RecencyWithSameDirRecall(c *Corpus, q Query) []Entry {
	es := c.Snapshot()

	n := q.N
	if n <= 0 || n > len(es) {
		n = len(es)
	}
	cut := len(es) - n
	recent := es[cut:]

	// Nothing older to reach into, or no directory to key on.
	if cut == 0 || q.Cwd == "" {
		return recent
	}

	// Walk backwards from just before the window so the K entries kept are
	// the most recent same-dir ones, not the oldest.
	recalled := make([]Entry, 0, DefaultRecallK)
	for i := cut - 1; i >= 0 && len(recalled) < DefaultRecallK; i-- {
		if es[i].Cwd == q.Cwd {
			recalled = append(recalled, es[i])
		}
	}
	if len(recalled) == 0 {
		return recent
	}
	slices.Reverse(recalled) // collected newest-first; the pool is oldest-first

	return append(recalled, recent...)
}
