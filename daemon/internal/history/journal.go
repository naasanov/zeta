package history

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
)

// DefaultJournalMax is how many lines the on-disk journal holds before it is
// rewritten to its newest half. Separate knob from DefaultCorpusMax: one
// bounds query cost, the other bounds bytes on disk.
const DefaultJournalMax = 10000

// journalChanBuf sizes the writer channel. One record per executed command
// is a trickle, so this only exists so a write can never block a shell.
const journalChanBuf = 256

// journal appends entries to a JSONL file from a single writer goroutine.
// The concurrency shape is copied from internal/metrics, not imported —
// metrics must stay a leaf. Mode 0600: the file holds unredacted commands.
type journal struct {
	path string
	max  int

	mu  sync.Mutex // guards f/enc; also stops Close racing the final write
	f   *os.File
	enc *json.Encoder

	lines int // written since open; drives rotation, touched only by run()

	ch        chan Entry
	drops     atomic.Int64
	done      chan struct{}
	closeOnce sync.Once

	// closeMu guards write racing close: closing ch mid-send panics, and
	// neither select/default nor an atomic flag prevents that.
	closeMu sync.RWMutex
	closed  bool
}

// openJournal opens (creating if needed) the JSONL journal at path and
// starts its writer goroutine. seen is the number of records already on
// disk, so rotation accounts for them.
func openJournal(path string, max, seen int) (*journal, error) {
	if max <= 0 {
		max = DefaultJournalMax
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}

	j := &journal{
		path:  path,
		max:   max,
		f:     f,
		lines: seen,
		ch:    make(chan Entry, journalChanBuf),
		done:  make(chan struct{}),
	}
	j.enc = newJSONEncoder(f)
	go j.run()
	return j, nil
}

// newJSONEncoder disables HTML escaping for the same reason protocol.Encode
// does: shell commands are full of '<', '>' and '&', and \uXXXX in the
// journal would be both unreadable and lossy to re-read.
func newJSONEncoder(f *os.File) *json.Encoder {
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	return enc
}

// write hands e to the writer goroutine. Never blocks: drops and counts if
// the buffer is full. Safe on a nil or closed journal.
func (j *journal) write(e Entry) {
	if j == nil {
		return
	}
	j.closeMu.RLock()
	defer j.closeMu.RUnlock()
	if j.closed {
		return
	}
	select {
	case j.ch <- e:
	default:
		j.drops.Add(1)
	}
}

// drops reports how many entries were dropped because the buffer was full.
func (j *journal) dropped() int64 {
	if j == nil {
		return 0
	}
	return j.drops.Load()
}

// run is the single writer goroutine. Rotation happens here so it is always
// off the request path.
func (j *journal) run() {
	defer close(j.done)
	for e := range j.ch {
		j.mu.Lock()
		if err := j.enc.Encode(e); err == nil {
			j.lines++
		}
		j.mu.Unlock()

		if j.lines > j.max {
			// Best-effort: a failed rotation leaves an oversized journal,
			// which is harmless, so keep accepting writes either way.
			_ = j.rotate()
		}
	}
}

// rotate rewrites the journal to its newest max/2 records via a temp file and
// an atomic rename, so a concurrent reader never observes a partial file.
// Only run() calls this.
func (j *journal) rotate() error {
	es, err := readJournal(j.path)
	if err != nil {
		return err
	}
	keep := max(j.max/2, 1)
	if len(es) <= keep {
		j.mu.Lock()
		j.lines = len(es)
		j.mu.Unlock()
		return nil
	}
	es = es[len(es)-keep:]

	tmp := j.path + ".tmp"
	tf, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	enc := newJSONEncoder(tf)
	for _, e := range es {
		if err := enc.Encode(e); err != nil {
			tf.Close()
			os.Remove(tmp)
			return err
		}
	}
	if err := tf.Close(); err != nil {
		os.Remove(tmp)
		return err
	}

	j.mu.Lock()
	defer j.mu.Unlock()
	// The appending fd points at the old inode, so it must be swung onto the
	// new file after the rename or subsequent writes vanish.
	j.f.Close()
	if err := os.Rename(tmp, j.path); err != nil {
		os.Remove(tmp)
		// Reopen the original so writes keep landing somewhere.
		if f, rerr := os.OpenFile(j.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); rerr == nil {
			j.f, j.enc = f, newJSONEncoder(f)
		}
		return err
	}
	f, err := os.OpenFile(j.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	j.f, j.enc, j.lines = f, newJSONEncoder(f), len(es)
	return nil
}

// close drains buffered entries, stops the writer, and closes the file.
// Repeat calls are safe.
func (j *journal) close() error {
	if j == nil {
		return nil
	}
	var err error
	j.closeOnce.Do(func() {
		j.closeMu.Lock()
		j.closed = true
		j.closeMu.Unlock()

		close(j.ch)
		<-j.done
		j.mu.Lock()
		err = j.f.Close()
		j.mu.Unlock()
	})
	return err
}

// readJournal reads every entry from a JSONL journal, oldest-first. A
// truncated or corrupt final line (a crash mid-write) is skipped rather than
// failing the read: losing one record beats refusing to start.
func readJournal(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), maxRecordBytes)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		if e.Cmd == "" {
			continue
		}
		out = append(out, e)
	}
	if err := sc.Err(); err != nil {
		return out, err
	}
	return out, nil
}
