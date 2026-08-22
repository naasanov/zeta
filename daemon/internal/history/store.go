package history

import (
	"log/slog"
	"os"
)

// Config is everything Store needs. All of it is resolved by the caller —
// this package reads no environment.
type Config struct {
	// JournalPath is the JSONL file the daemon owns. Its ABSENCE is the
	// bootstrap watermark: there is no separate marker file.
	JournalPath string

	// HistfilePath is read exactly once, on the very first start, to seed a
	// corpus that would otherwise be empty for days. Empty skips bootstrap.
	HistfilePath string

	CorpusMax  int // in-memory entries; <= 0 selects DefaultCorpusMax
	JournalMax int // on-disk lines;    <= 0 selects DefaultJournalMax

	Log *slog.Logger
}

// Store is the package facade: lifecycle and wiring. The corpus stays pure
// and the journal stays invisible to callers.
type Store struct {
	corpus *Corpus
	j      *journal
	log    *slog.Logger

	// Retrieve is the candidate-pool policy. A function field so it can be
	// swapped in one line, and so a test can install its own.
	Retrieve Retriever
}

// New loads the corpus and starts the journal writer. Bootstrap runs once
// ever: with no journal file, $HISTFILE seeds both the corpus and the new
// journal, and the journal is the only source thereafter — the overlap
// between the two is not reliably detectable.
func New(cfg Config) (*Store, error) {
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}

	var (
		seed         []Entry
		didBootstrap bool
	)
	if _, err := os.Stat(cfg.JournalPath); err == nil {
		if seed, err = readJournal(cfg.JournalPath); err != nil {
			log.Warn("history: reading journal failed, starting empty",
				"path", cfg.JournalPath, "err", err)
			seed = nil
		}
	} else if os.IsNotExist(err) && cfg.HistfilePath != "" {
		didBootstrap = true
		// Never fatal, and a partial parse is kept: a shell with some history
		// context beats one with none.
		seed, err = bootstrapFromHistfile(cfg.HistfilePath, cfg.CorpusMax)
		if err != nil {
			log.Warn("history: $HISTFILE bootstrap incomplete",
				"path", cfg.HistfilePath, "entries", len(seed), "err", err)
		} else {
			log.Info("history: bootstrapped from histfile",
				"path", cfg.HistfilePath, "entries", len(seed))
		}
	}

	j, err := openJournal(cfg.JournalPath, cfg.JournalMax, len(seed))
	if err != nil {
		return nil, err
	}

	s := &Store{
		corpus:   NewCorpus(cfg.CorpusMax),
		j:        j,
		log:      log,
		Retrieve: RecencyWithSameDirRecall,
	}
	s.corpus.Replay(seed)

	if didBootstrap {
		for _, e := range seed {
			j.write(e)
		}
	}
	return s, nil
}

// Record adds one executed command to the corpus and queues it for the
// journal. Never blocks.
func (s *Store) Record(e Entry) {
	if s == nil || e.Cmd == "" {
		return
	}
	s.corpus.Append(e)
	s.j.write(e)
}

// Select returns the candidate pool for one request, oldest-first. Callers
// truncate it themselves — this is a pool, not a render list.
func (s *Store) Select(q Query) []Entry {
	if s == nil {
		return nil
	}
	r := s.Retrieve
	if r == nil {
		r = RecencyWithSameDirRecall
	}
	return r(s.corpus, q)
}

// Len reports the corpus size, for logging and tests.
func (s *Store) Len() int {
	if s == nil {
		return 0
	}
	return s.corpus.Len()
}

// Close flushes and stops the journal writer.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	if d := s.j.dropped(); d > 0 {
		s.log.Warn("history: journal entries dropped", "count", d)
	}
	return s.j.close()
}
