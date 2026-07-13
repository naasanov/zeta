// Package server implements the autopilotd process skeleton: a Unix-socket
// listener that speaks the protocol package's wire format. This is the Phase
// 1 step 2 skeleton — it answers every request with a canned echo suggestion
// (see suggestEcho) so the protocol and process lifecycle can be validated
// end-to-end against the real daemon before the provider layer exists. Later
// steps (request coordinator, debounce, provider) build on top of Server
// without changing its shape.
package server

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"

	"github.com/naasanov/zsh-autopilot/daemon/internal/protocol"
)

// DefaultSocket is a fallback for hand-running the daemon (and tests). In the
// real flow the zsh plugin owns the path: it spawns the daemon with an explicit
// -socket and dials that same path, so this default is never the rendezvous
// point in practice. Kept short because macOS caps socket paths at ~104 bytes.
const DefaultSocket = "/tmp/zsh-autopilot.sock"

// Server listens on a Unix domain socket and answers each request with a
// suggestion. It holds no provider state yet; that lands in a later step.
type Server struct {
	SocketPath string
	Log        *slog.Logger

	mu    sync.Mutex
	conns map[net.Conn]struct{}
}

// New returns a Server configured to listen on path, logging via log. If log
// is nil, slog.Default() is used.
func New(path string, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{
		SocketPath: path,
		Log:        log,
		conns:      make(map[net.Conn]struct{}),
	}
}

// Run binds the socket, accepts connections until ctx is cancelled, and
// cleans up on the way out (closing the listener, closing in-flight
// connections, and removing the socket file). It returns a non-nil error if
// another daemon is already listening on SocketPath (single-instance guard)
// or if the listener cannot be created.
func (s *Server) Run(ctx context.Context) error {
	if err := s.claimSocket(); err != nil {
		return err
	}

	ln, err := net.Listen("unix", s.SocketPath)
	if err != nil {
		return err
	}
	// Best-effort cleanup; also done explicitly below once accept loop exits.
	defer os.Remove(s.SocketPath)

	s.Log.Info("listening", "socket", s.SocketPath)

	// Stop accepting and unblock Accept() when ctx is cancelled.
	go func() {
		<-ctx.Done()
		s.Log.Debug("context cancelled, closing listener")
		ln.Close()
	}()

	var wg sync.WaitGroup
	for {
		conn, err := ln.Accept()
		if err != nil {
			// Expected once ctx cancellation closes the listener; anything
			// else is a real accept error.
			select {
			case <-ctx.Done():
				// Close in-flight connections first to unblock handlers parked
				// in Decode (the client's warm socket), THEN wait for them.
				// The reverse order deadlocks: Wait never returns while a
				// handler is still blocked on an open connection.
				s.closeAllConns()
				wg.Wait()
				os.Remove(s.SocketPath)
				return nil
			default:
				s.Log.Error("accept", "err", err)
				continue
			}
		}

		s.trackConn(conn)
		wg.Go(func() {
			defer s.untrackConn(conn)
			s.handle(conn)
		})
	}
}

// claimSocket implements the single-instance guard: if SocketPath exists and
// something answers a dial, a live daemon already owns it, so we refuse to
// start. If nothing answers (a stale socket file left by a crashed daemon),
// remove it so net.Listen can bind cleanly.
func (s *Server) claimSocket() error {
	if _, err := os.Stat(s.SocketPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	conn, err := net.Dial("unix", s.SocketPath)
	if err == nil {
		conn.Close()
		s.Log.Error("another instance is already listening", "socket", s.SocketPath)
		return errors.New("autopilotd: socket " + s.SocketPath + " is already in use by a running daemon")
	}

	// Dial failed: stale socket from a crashed daemon. Remove it.
	s.Log.Debug("removing stale socket", "socket", s.SocketPath)
	return os.Remove(s.SocketPath)
}

func (s *Server) trackConn(c net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conns[c] = struct{}{}
}

func (s *Server) untrackConn(c net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.conns, c)
}

func (s *Server) closeAllConns() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.conns {
		c.Close()
	}
}

// handle reads newline-framed requests off conn and answers each with one
// reply, until EOF or a decode error ends the connection.
func (s *Server) handle(conn net.Conn) {
	defer conn.Close()
	s.Log.Debug("accepted connection", "remote", conn.RemoteAddr())

	dec := protocol.NewDecoder(conn)
	for {
		var req protocol.Request
		if err := dec.Decode(&req); err != nil {
			if errors.Is(err, net.ErrClosed) {
				s.Log.Debug("connection closed")
			} else {
				s.Log.Debug("decode", "err", err)
			}
			return
		}
		s.Log.Debug("request", "id", req.ID, "kind", req.Kind, "buf", req.Buf)

		reply := protocol.Reply{
			V:          protocol.Version,
			ID:         req.ID,
			Source:     protocol.SourceLLM,
			Suggestion: suggestEcho(req.Buf),
		}
		if err := protocol.Encode(conn, reply); err != nil {
			s.Log.Debug("encode", "err", err)
			return
		}
	}
}

// suggestEcho is a PLACEHOLDER echo suggestion, ported from
// spike/echo-server's suggest(). It exists only to prove the protocol and
// process skeleton end-to-end; a later step replaces this with the real
// provider layer. The reply MUST begin with the buffer itself, because the
// zsh client paints only the remainder after stripping the typed prefix.
func suggestEcho(buf string) string {
	switch {
	case buf == "":
		// Empty buffer = next-command request (fired from the zsh precmd
		// hook). Suggest a whole command; the client paints all of it.
		return "git status"
	case strings.HasPrefix(buf, "git"):
		return buf + " --oneline"
	case strings.HasPrefix(buf, "cd"):
		return buf + " ~/projects"
	case strings.HasPrefix(buf, "docker"):
		return buf + " ps -a"
	default:
		return buf + " # suggested by autopilotd"
	}
}
