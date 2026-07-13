// Command echo-server is the Phase 0/1 fake backend (design doc §11).
//
// It lets us exercise the zsh client — POSTDISPLAY ghost text, the `zle -F`
// async round-trip, accept/clear, precmd next-command firing, and now the
// newline-delimited JSON wire protocol with request-id correlation and the
// source tag — WITHOUT any real LLM or the real Go daemon.
//
// It speaks the same wire format as daemon/internal/protocol. Because this is a
// throwaway module (stdlib only, isolated go.mod), the message structs are
// duplicated inline rather than importing that package; keep them in sync with
// it until the real daemon retires this server in Phase 1 step 2.
//
// Run:  go run ./spike/echo-server -socket /tmp/zsh-autopilot.sock
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"log"
	"net"
	"os"
	"strings"
)

// request mirrors protocol.Request.
type request struct {
	V    int    `json:"v"`
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Buf  string `json:"buf"`
}

// reply mirrors protocol.Reply.
type reply struct {
	V          int    `json:"v"`
	ID         string `json:"id"`
	Source     string `json:"source"`
	Suggestion string `json:"suggestion"`
}

func main() {
	socket := flag.String("socket", "/tmp/zsh-autopilot.sock", "unix socket path to listen on")
	flag.Parse()

	// Clean up any stale socket from a previous run.
	_ = os.Remove(*socket)

	ln, err := net.Listen("unix", *socket)
	if err != nil {
		log.Fatalf("listen %s: %v", *socket, err)
	}
	defer ln.Close()
	log.Printf("echo-server listening on %s", *socket)

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("accept: %v", err)
			continue
		}
		go handle(conn)
	}
}

// handle reads newline-framed JSON requests and answers each with one JSON
// reply. Swap the suggestion logic freely while spiking the zsh client.
func handle(conn net.Conn) {
	defer conn.Close()
	sc := bufio.NewScanner(conn)
	// HTML escaping OFF so shell metacharacters (< > &) stay literal — the zsh
	// decoder doesn't handle \uXXXX. This mirrors protocol.Encode.
	enc := json.NewEncoder(conn)
	enc.SetEscapeHTML(false)

	for sc.Scan() {
		var req request
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			log.Printf("bad request %q: %v", sc.Text(), err)
			continue
		}
		log.Printf("recv id=%s kind=%s buf=%q", req.ID, req.Kind, req.Buf)

		// json.Encoder.Encode appends '\n', which is the frame the zsh
		// `IFS= read -r` callback splits on.
		if err := enc.Encode(reply{
			V:          1,
			ID:         req.ID, // echo the id back so the client can correlate
			Source:     "llm",
			Suggestion: suggest(req.Buf),
		}); err != nil {
			return
		}
	}
	if err := sc.Err(); err != nil {
		log.Printf("read: %v", err)
	}
}

// suggest returns a fake completion for the given buffer. The reply MUST begin
// with the buffer itself, because the zsh client paints only the remainder
// after stripping the typed prefix (POSTDISPLAY="${suggestion#$BUFFER}"). So we
// always return `buf + <appended text>`; that appended text is what shows up as
// grey ghost text. A few prefixes get flavored completions so accept/partial-
// accept can be exercised against varied suggestions.
func suggest(buf string) string {
	switch {
	case buf == "":
		// Empty buffer = next-command request (fired from the zsh precmd hook).
		// Suggest a whole command; on an empty buffer the client paints all of it.
		return "git status"
	case strings.HasPrefix(buf, "git"):
		return buf + " --oneline"
	case strings.HasPrefix(buf, "cd"):
		return buf + " ~/projects"
	case strings.HasPrefix(buf, "docker"):
		return buf + " ps -a"
	default:
		return buf + " # suggested by echo-server"
	}
}
