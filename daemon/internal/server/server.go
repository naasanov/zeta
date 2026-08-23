// Package server implements the autopilotd process: a Unix-socket listener
// speaking the protocol package's wire format, plus a per-connection request
// coordinator (design §3, §13 "Goroutine/cancellation leaks"). Within one
// connection, a newly arriving request supersedes and cancels the previous
// in-flight one via context.Context, so stale work never blocks a fresher
// request or leaks a goroutine.
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
	"time"

	"github.com/naasanov/zsh-autopilot/daemon/internal/protocol"
)

// DefaultSocket is a fallback for hand-running the daemon (and tests). In the
// real flow the zsh plugin owns the path: it spawns the daemon with an explicit
// -socket and dials that same path, so this default is never the rendezvous
// point in practice. Kept short because macOS caps socket paths at ~104 bytes.
const DefaultSocket = "/tmp/zsh-autopilot.sock"

// DefaultDebounce is the default quiet period a connection must see before a
// buffered request is dispatched to the provider (design §4). Rapid keystroke
// bursts within this window collapse into a single dispatch of the LATEST
// buffer, which is what keeps us under free-tier provider rate limits
// (cancellation alone doesn't help: a cancelled request was already sent and
// still counts against the limit).
const DefaultDebounce = 100 * time.Millisecond

// Server listens on a Unix domain socket and answers each request with a
// suggestion. It holds no provider state yet; that lands in a later step.
type Server struct {
	SocketPath string
	Log        *slog.Logger
	// Debounce is the quiet period before a buffered request dispatches (see
	// DefaultDebounce). Zero means "unset"; New fills in DefaultDebounce, but
	// tests within this package may still set it directly to something small
	// for speed.
	Debounce time.Duration

	mu    sync.Mutex
	conns map[net.Conn]struct{}

	// suggest produces the reply for a request; defaults to an instant echo
	// stub, overridden via SetSuggest. It MUST respect ctx: return promptly
	// when ctx is done so a superseded/cancelled request's goroutine doesn't
	// leak.
	suggest func(ctx context.Context, req protocol.Request) (protocol.Reply, error)

	// record handles a KindRecord request; defaults to a no-op, overridden
	// via SetRecord. It runs synchronously on the reader goroutine, so it
	// must not block, and it never produces a Reply.
	record func(req protocol.Request)
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
		Debounce:   DefaultDebounce,
		conns:      make(map[net.Conn]struct{}),
		suggest:    suggestInstantEcho,
		record:     func(protocol.Request) {},
	}
}

// SetSuggest overrides the suggest func used to answer requests, replacing
// the default echo stub. main installs the real provider-backed suggester
// here once a Client is configured (see cmd/autopilotd); call it before Run.
// Tests within this package may still set the unexported field directly.
func (s *Server) SetSuggest(fn func(ctx context.Context, req protocol.Request) (protocol.Reply, error)) {
	s.suggest = fn
}

// SetRecord overrides the record func invoked for KindRecord requests,
// replacing the default no-op. main installs the history store's Record
// method here (see cmd/autopilotd). Call it before Run.
func (s *Server) SetRecord(fn func(req protocol.Request)) {
	s.record = fn
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

// shortID trims the constant per-session prefix from a request id for terse
// logs. IDs are "<session>.<seq>" (the client mints them, §protocol); the
// session part repeats on every request over a connection, so only the trailing
// sequence number carries information line-to-line.
func shortID(id string) string {
	if i := strings.LastIndexByte(id, '.'); i >= 0 {
		return id[i+1:]
	}
	return id
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

// handle owns one connection's request lifecycle: a reader goroutine decodes
// requests off the wire and hands them to this goroutine (the coordinator),
// which debounces bursts and dispatches processing goroutines, until EOF/a
// decode error ends the connection or the server shuts down.
//
// Debounce (design §4): a burst of rapid requests must not each hit the
// provider — free-tier rate limits make that fail fast, and cancellation
// alone doesn't help since a cancelled request was already sent and still
// counted against the limit. So dispatch only happens once s.Debounce has
// passed since the latest buffered request with nothing superseding it.
//
// Dispatch, cancelPrev, and the debounce timer are all owned by this single
// goroutine (never the reader) so teardown can wait on wg without a timer
// callback racing wg.Add against wg.Wait from elsewhere.
//
// Per-connection state: connCtx/connCancel tear down every processing
// goroutine at the latest by connection teardown; cancelPrev is the
// supersede handle (only this goroutine touches it, no mutex needed);
// writeMu serializes writes to conn; wg tracks outstanding processing
// goroutines so handle doesn't return (racing conn.Close against a write)
// until they've all exited.
func (s *Server) handle(ctx context.Context, conn net.Conn) {
	connCtx, connCancel := context.WithCancel(ctx)

	var writeMu sync.Mutex
	var wg sync.WaitGroup
	// cancelPrev cancels the previous in-flight request when a new one is
	// dispatched (supersede). Only the coordinator loop below touches it —
	// no mutex needed.
	var cancelPrev context.CancelFunc

	// Defers run LIFO, so declaration order here is deliberate: on return we
	// want connCancel() (stop any in-flight request, and unblock the reader
	// goroutine's channel send if it's parked there) -> wg.Wait() (let
	// processing goroutines actually exit) -> conn.Close() (only now is it
	// safe; no goroutine can still be writing to conn). conn.Close() also
	// unblocks the reader goroutine's Decode call so it can exit.
	defer conn.Close()
	defer wg.Wait()
	defer connCancel()

	s.Log.Debug("accepted connection", "remote", conn.RemoteAddr())

	debounce := s.Debounce
	if debounce <= 0 {
		debounce = DefaultDebounce
	}

	// reqCh carries decoded requests from the reader goroutine to this
	// coordinator. It's closed by the reader when Decode ends (EOF/error),
	// which signals the coordinator to tear down.
	reqCh := make(chan protocol.Request)
	go func() {
		defer close(reqCh)
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
			s.Log.Debug("request", "id", shortID(req.ID), "kind", req.Kind, "buf", req.Buf)
			if req.Kind == protocol.KindRecord {
				s.record(req)
				continue
			}
			select {
			case reqCh <- req:
			case <-connCtx.Done():
				// Coordinator is tearing down; don't block forever trying to
				// hand off a request nobody will read.
				return
			}
		}
	}()

	// timer drives dispatch: it fires debounce after the most recently
	// buffered request, unless a newer request resets it first. Created
	// stopped so it never fires before the first request arrives.
	timer := time.NewTimer(debounce)
	timer.Stop()
	defer timer.Stop()

	var pending *protocol.Request

	for {
		select {
		case req, ok := <-reqCh:
			if !ok {
				// Reader ended (EOF/decode error): tear down.
				return
			}
			r := req
			pending = &r
			timer.Reset(debounce)

		case <-timer.C:
			if pending == nil {
				continue
			}
			req := *pending
			pending = nil

			// One dispatch per debounce window — this is the request that
			// actually reaches the suggester/provider, so watching these lines
			// vs. the per-keystroke "request" lines shows debounce coalescing.
			s.Log.Debug("dispatch", "id", shortID(req.ID), "kind", req.Kind, "buf", req.Buf)

			// Supersede: cancel whatever was previously in flight on this
			// connection, then remember this request's cancel for next
			// time. Dispatch only ever happens here, in the coordinator
			// goroutine, so this needs no mutex.
			if cancelPrev != nil {
				cancelPrev()
			}
			reqCtx, reqCancel := context.WithCancel(connCtx)
			cancelPrev = reqCancel

			wg.Go(func() {
				defer reqCancel()

				reply, err := s.suggest(reqCtx, req)

				// Cancelled/superseded/connection-closing: don't write.
				// It's fine if this races a request that finished
				// computing just as it was cancelled (see design note);
				// the client drops replies whose id isn't its current
				// one, so a stray stale write would be harmless too, but
				// skipping it is just as easy and avoids writing on
				// behalf of a request nobody wants anymore.
				if reqCtx.Err() != nil {
					return
				}
				if err != nil {
					s.Log.Debug("suggest", "id", shortID(req.ID), "err", err)
					return
				}

				writeMu.Lock()
				defer writeMu.Unlock()
				if err := protocol.Encode(conn, reply); err != nil {
					s.Log.Debug("encode", "err", err)
				}
			})

		case <-connCtx.Done():
			// Server shutdown or connection teardown initiated elsewhere.
			return
		}
	}
}

// suggestEcho is the default (no-provider) echo suggestion. The reply MUST
// begin with the buffer itself: the zsh client paints only the remainder
// after stripping the typed prefix.
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
