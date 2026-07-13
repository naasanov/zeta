// Package server implements the autopilotd process: a Unix-socket listener
// that speaks the protocol package's wire format and a per-connection request
// coordinator (design §3, §13 "Goroutine/cancellation leaks"). Within one
// connection (one shell session), a newly arriving request supersedes and
// cancels the previous in-flight one via context.Context cancellation, so
// stale work never blocks a fresher request or leaks a goroutine. The
// suggestion source itself (see suggest / suggestEcho) is still a stub — the
// real provider layer lands in a later step and only needs to replace that
// one seam.
package server

import (
	"context"
	"errors"
	"io"
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

	// suggest produces the reply for a request. This is the seam the real
	// provider layer replaces in a later step; today it defaults to an
	// instant echo stub. Tests override it with a controlled stub (one that
	// blocks on ctx, or on a per-request channel) to create deterministic
	// cancellation windows. It MUST respect ctx: return promptly when ctx is
	// done so a superseded/cancelled request's goroutine doesn't leak.
	suggest func(ctx context.Context, req protocol.Request) (protocol.Reply, error)
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
		suggest:    suggestInstantEcho,
	}
}

// suggestInstantEcho is the default suggest func: an instant (non-blocking)
// echo suggestion, so the plain end-to-end echo path (no provider yet) keeps
// working with no added latency. It ignores ctx because it never blocks.
func suggestInstantEcho(_ context.Context, req protocol.Request) (protocol.Reply, error) {
	return protocol.Reply{
		V:          protocol.Version,
		ID:         req.ID,
		Source:     protocol.SourceLLM,
		Suggestion: suggestEcho(req.Buf),
	}, nil
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
			s.handle(ctx, conn)
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

// handle reads newline-framed requests off conn continuously and dispatches
// each to its own processing goroutine, until EOF or a decode error ends the
// connection. It never blocks on processing a request — that's the whole
// point of the coordinator: a request that's slow to answer (real provider
// call, later step) must not stop the reader from seeing the NEXT request,
// because that next request is what supersedes and cancels the slow one.
//
// Per-connection state:
//   - connCtx/connCancel: cancelled when the server shuts down (ctx) OR this
//     handler returns (EOF/close), so every processing goroutine for this
//     connection is torn down at the latest by connection teardown.
//   - cancelPrev: the supersede handle. A newly dispatched request cancels
//     whatever was previously in flight before installing itself. Only this
//     connection's reader loop touches it, so it needs no mutex.
//   - writeMu: serializes writes to conn so two processing goroutines never
//     interleave bytes on the wire.
//   - wg: tracks outstanding processing goroutines so handle does not return
//     (and does not let conn.Close race a write) until they've all exited.
func (s *Server) handle(ctx context.Context, conn net.Conn) {
	connCtx, connCancel := context.WithCancel(ctx)

	var writeMu sync.Mutex
	var wg sync.WaitGroup
	// cancelPrev cancels the previous in-flight request when a new one arrives
	// (supersede). Only this connection's reader loop touches it — no mutex.
	var cancelPrev context.CancelFunc

	// Defers run LIFO, so declaration order here is deliberate: on return we
	// want connCancel() (stop any in-flight request) -> wg.Wait() (let its
	// goroutine actually exit) -> conn.Close() (only now is it safe; no
	// goroutine can still be writing to conn).
	defer conn.Close()
	defer wg.Wait()
	defer connCancel()

	s.Log.Debug("accepted connection", "remote", conn.RemoteAddr())

	dec := protocol.NewDecoder(conn)
	for {
		var req protocol.Request
		if err := dec.Decode(&req); err != nil {
			if errors.Is(err, net.ErrClosed) || errors.Is(err, io.EOF) {
				s.Log.Debug("connection closed")
			} else {
				s.Log.Debug("decode", "err", err)
			}
			return
		}
		s.Log.Debug("request", "id", req.ID, "kind", req.Kind, "buf", req.Buf)

		// Supersede: cancel whatever was previously in flight on this
		// connection, then remember this request's cancel for next time.
		if cancelPrev != nil {
			cancelPrev()
		}
		reqCtx, reqCancel := context.WithCancel(connCtx)
		cancelPrev = reqCancel

		// req is declared fresh each iteration (var req protocol.Request
		// above, inside the loop body), so it's already safe to capture
		// per-goroutine — no shadowing dance needed here.
		wg.Go(func() {
			defer reqCancel()

			reply, err := s.suggest(reqCtx, req)

			// Cancelled/superseded/connection-closing: don't write. It's
			// fine if this races a request that finished computing just as
			// it was cancelled (see design note); the client drops replies
			// whose id isn't its current one, so a stray stale write would
			// be harmless too, but skipping it is just as easy and avoids
			// writing on behalf of a request nobody wants anymore.
			if reqCtx.Err() != nil {
				return
			}
			if err != nil {
				s.Log.Debug("suggest", "id", req.ID, "err", err)
				return
			}

			writeMu.Lock()
			defer writeMu.Unlock()
			if err := protocol.Encode(conn, reply); err != nil {
				s.Log.Debug("encode", "err", err)
			}
		})
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
