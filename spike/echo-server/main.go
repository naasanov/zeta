// Command echo-server is the Phase 0 fake backend (design doc §11, Phase 0).
//
// It lets us de-risk the scary zsh primitives — POSTDISPLAY ghost text, the
// `zle -F` async round-trip, accept/clear, and precmd next-command firing —
// WITHOUT any real LLM or the real Go daemon. It listens on a Unix socket and
// replies to each request with a trivial canned/derived suggestion so the shell
// side can be exercised end to end.
//
// Run:  go run ./spike/echo-server -socket /tmp/zsh-autopilot.sock
//
// This is throwaway scaffolding; the real daemon lives in cmd/autopilotd.
package main

import (
	"bufio"
	"flag"
	"log"
	"net"
	"os"
	"strings"
)

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

// handle replies to every newline-framed request with a fake suggestion. Swap
// the reply logic freely while spiking the zsh client.
func handle(conn net.Conn) {
	defer conn.Close()
	sc := bufio.NewScanner(conn)
	for sc.Scan() {
		line := sc.Text()
		log.Printf("recv: %q", line)
		// Reply = suggestion + newline. The newline is the frame the zsh
		// `IFS= read -r` callback splits on; without it the client blocks.
		if _, err := conn.Write([]byte(suggest(line) + "\n")); err != nil {
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
// always return `line + <appended text>`; that appended text is what shows up as
// grey ghost text. A few prefixes get flavored completions so accept/partial-
// accept can be exercised against varied suggestions.
func suggest(line string) string {
	switch {
	case line == "":
		// Empty buffer = next-command request (fired from the zsh precmd hook).
		// Suggest a whole command; on an empty buffer the client paints all of it.
		return "git status"
	case strings.HasPrefix(line, "git"):
		return line + " --oneline"
	case strings.HasPrefix(line, "cd"):
		return line + " ~/projects"
	case strings.HasPrefix(line, "docker"):
		return line + " ps -a"
	default:
		return line + " # suggested by echo-server"
	}
}
