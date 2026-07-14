package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
)

// compactHandler is a terse slog.Handler for the dev log panel. Instead of
// slog's default `time=... level=... msg=... key=val`, it prints
//
//	HH:MM:SS.mmm L msg=... key=val ...
//
// dropping the time=/level= keys and shortening the level to one letter
// (D/I/W/E), while keeping msg= and the structured attrs' key=value form,
// quoted (Go-style) when a value contains spaces, `=`, `"`, or is empty. It's
// dev tooling; the shipped build can swap back to a JSON/text handler if
// machine-readable logs are ever wanted.
type compactHandler struct {
	mu    *sync.Mutex // shared across WithAttrs/WithGroup clones so writes stay serialized
	w     io.Writer
	level slog.Level
	attrs string // preformatted preset attrs (each with a leading space)
	group string // dotted group prefix for keys, "" if none
}

func newCompactHandler(w io.Writer, level slog.Level) *compactHandler {
	return &compactHandler{mu: &sync.Mutex{}, w: w, level: level}
}

func (h *compactHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level
}

func levelChar(l slog.Level) byte {
	switch {
	case l >= slog.LevelError:
		return 'E'
	case l >= slog.LevelWarn:
		return 'W'
	case l >= slog.LevelInfo:
		return 'I'
	default:
		return 'D'
	}
}

func (h *compactHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Time.Format("15:04:05.000"))
	b.WriteByte(' ')
	b.WriteByte(levelChar(r.Level))
	b.WriteString(" msg=")
	writeValue(&b, r.Message)
	b.WriteString(h.attrs)
	r.Attrs(func(a slog.Attr) bool {
		h.appendAttr(&b, h.group, a)
		return true
	})
	b.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.w, b.String())
	return err
}

// appendAttr writes " key=value" (with group prefix), recursing into groups.
func (h *compactHandler) appendAttr(b *strings.Builder, group string, a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return
	}
	key := a.Key
	if group != "" && key != "" {
		key = group + "." + key
	}
	if a.Value.Kind() == slog.KindGroup {
		for _, ga := range a.Value.Group() {
			h.appendAttr(b, key, ga)
		}
		return
	}
	b.WriteByte(' ')
	b.WriteString(key)
	b.WriteByte('=')
	writeValue(b, a.Value.String())
}

// writeValue writes a value bare, or Go-quoted when it's empty or contains
// characters (space, `=`, `"`, newline) that would blur field boundaries.
func writeValue(b *strings.Builder, val string) {
	if val == "" || strings.ContainsAny(val, " \t\n\"=") {
		fmt.Fprintf(b, "%q", val)
	} else {
		b.WriteString(val)
	}
}

func (h *compactHandler) WithAttrs(as []slog.Attr) slog.Handler {
	if len(as) == 0 {
		return h
	}
	var b strings.Builder
	b.WriteString(h.attrs)
	for _, a := range as {
		h.appendAttr(&b, h.group, a)
	}
	nh := *h
	nh.attrs = b.String()
	return &nh
}

func (h *compactHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	nh := *h
	if h.group != "" {
		nh.group = h.group + "." + name
	} else {
		nh.group = name
	}
	return &nh
}
