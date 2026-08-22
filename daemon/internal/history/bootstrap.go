package history

import (
	"bufio"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// maxRecordBytes caps one logical history record. Large enough for a pasted
// heredoc, small enough that a corrupt file cannot exhaust memory.
const maxRecordBytes = 4 * 1024 * 1024

// extendedPrefix matches zsh's EXTENDED_HISTORY line prefix,
// ": <started>:<elapsed>;". Note it carries a timestamp but NO directory —
// which is the whole reason cwd has to be captured going forward and why
// everything bootstrapped here is permanently Cwd:"".
var extendedPrefix = regexp.MustCompile(`^: (\d+):(\d+);`)

// bootstrapFromHistfile parses path as a zsh history file, returning the last
// max records oldest-first with Cwd and Session empty. On error it still
// returns whatever parsed; the caller logs and continues with that.
func bootstrapFromHistfile(path string, max int) ([]Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	es, err := parseHistfile(f)
	if max > 0 && len(es) > max {
		es = es[len(es)-max:]
	}
	return es, err
}

// parseHistfile turns a zsh history stream into entries. EXTENDED_HISTORY
// prefixes a record's first line with ": <ts>:<elapsed>;"; an embedded newline
// is a trailing backslash, escaped as two when literal, so a continuation is
// an ODD trailing count.
func parseHistfile(r io.Reader) ([]Entry, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxRecordBytes)

	var (
		out    []Entry
		buf    strings.Builder
		inCont bool
		ts     int64
	)

	flush := func() {
		cmd := strings.TrimSpace(buf.String())
		buf.Reset()
		if cmd != "" {
			out = append(out, Entry{Cmd: cmd, Ts: ts})
		}
	}

	for sc.Scan() {
		line := sc.Text()

		if !inCont {
			ts = 0
			if m := extendedPrefix.FindStringSubmatch(line); m != nil {
				ts, _ = strconv.ParseInt(m[1], 10, 64)
				line = line[len(m[0]):]
			}
			buf.Reset()
		}

		if oddTrailingBackslashes(line) {
			buf.WriteString(line[:len(line)-1])
			buf.WriteByte('\n')
			inCont = true
			continue
		}

		buf.WriteString(line)
		inCont = false
		flush()
	}

	// A file ending mid-continuation still yields the partial command; the
	// alternative is silently dropping the most recent thing the user ran.
	if inCont {
		flush()
	}
	// A line over maxRecordBytes, or a read error, stops Scan early. Return
	// what parsed alongside the error so a partial corpus is still usable.
	return out, sc.Err()
}

// oddTrailingBackslashes reports whether s ends in an odd number of
// backslashes, i.e. the final one escapes a newline rather than itself.
func oddTrailingBackslashes(s string) bool {
	n := 0
	for i := len(s) - 1; i >= 0 && s[i] == '\\'; i-- {
		n++
	}
	return n%2 == 1
}
