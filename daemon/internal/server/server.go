// Package server implements the autopilotd process's Unix-socket listener and request coordinator.
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

// DefaultSocket is a fallback default; macOS caps socket paths at ~104 bytes.
const DefaultSocket = "/tmp/zsh-autopilot.sock"

const DefaultDebounce = 100 * time.Millisecond

// Server listens on a Unix domain socket and answers each request with a
// suggestion.
type Server struct {
	SocketPath string
	Log        *slog.Logger
	// Zero means unset; New fills in DefaultDebounce.
	Debounce time.Duration

	mu    sync.Mutex
	conns map[net.Conn]struct{}

	// suggest MUST return promptly once ctx is done, or a superseded
	// request's goroutine leaks.
	suggest func(ctx context.Context, req protocol.Request) (protocol.Reply, error)

	// record runs on the reader goroutine, so it must not block.
	record func(req protocol.Request)

	notice func(err error) (text, kind string, ok bool)
}

func New(path string, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{
		SocketPath: path,
		Log:        log,
		Debounce:   DefaultDebounce,
		conns:      make(map[net.Conn]struct{}),
		suggest:    noSuggester,
		record:     func(protocol.Request) {},
		notice:     func(error) (string, string, bool) { return "", "", false },
	}
}

// Must be called before Run.
func (s *Server) SetSuggest(fn func(ctx context.Context, req protocol.Request) (protocol.Reply, error)) {
	s.suggest = fn
}

// Must be called before Run.
func (s *Server) SetRecord(fn func(req protocol.Request)) {
	s.record = fn
}

// Must be called before Run.
func (s *Server) SetNotice(fn func(error) (string, string, bool)) {
	s.notice = fn
}

func noSuggester(context.Context, protocol.Request) (protocol.Reply, error) {
	return protocol.Reply{}, errors.New("server: no suggester configured")
}

// Run binds the socket and accepts connections until ctx is cancelled, then
// tears down and removes the socket file; it errors if another daemon
// already owns SocketPath.
func (s *Server) Run(ctx context.Context) error {
	if err := s.claimSocket(); err != nil {
		return err
	}

	ln, err := net.Listen("unix", s.SocketPath)
	if err != nil {
		return err
	}
	defer os.Remove(s.SocketPath)

	s.Log.Info("listening", "socket", s.SocketPath)

	go func() {
		<-ctx.Done()
		s.Log.Debug("context cancelled, closing listener")
		ln.Close()
	}()

	var wg sync.WaitGroup
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				// Close before waiting, or handlers parked in Decode never
				// return and Wait deadlocks.
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

	s.Log.Debug("removing stale socket", "socket", s.SocketPath)
	return os.Remove(s.SocketPath)
}

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

func (s *Server) handle(ctx context.Context, conn net.Conn) {
	connCtx, connCancel := context.WithCancel(ctx)

	var writeMu sync.Mutex
	var wg sync.WaitGroup
	var cancelPrev context.CancelFunc

	// Defers run LIFO: connCancel unblocks the reader, wg.Wait lets writers
	// exit, then conn.Close is safe.
	defer conn.Close()
	defer wg.Wait()
	defer connCancel()

	s.Log.Debug("accepted connection", "remote", conn.RemoteAddr())

	debounce := s.Debounce
	if debounce <= 0 {
		debounce = DefaultDebounce
	}

	reqCh := s.readRequests(connCtx, conn)

	timer := time.NewTimer(debounce)
	timer.Stop()
	defer timer.Stop()

	var pending *protocol.Request

	for {
		select {
		case req, ok := <-reqCh:
			if !ok {
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

			s.Log.Debug("dispatch", "id", shortID(req.ID), "kind", req.Kind, "buf", req.Buf)

			if cancelPrev != nil {
				cancelPrev()
			}
			reqCtx, reqCancel := context.WithCancel(connCtx)
			cancelPrev = reqCancel

			wg.Go(func() {
				defer reqCancel()
				s.respond(reqCtx, conn, &writeMu, req)
			})

		case <-connCtx.Done():
			return
		}
	}
}

func (s *Server) readRequests(connCtx context.Context, conn net.Conn) <-chan protocol.Request {
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
				return
			}
		}
	}()
	return reqCh
}

func (s *Server) respond(ctx context.Context, conn net.Conn, writeMu *sync.Mutex, req protocol.Request) {
	reply, err := s.suggest(ctx, req)

	if err != nil {
		s.reportSuggestErr(conn, writeMu, req, err)
		return
	}

	if ctx.Err() != nil {
		return
	}

	s.writeReply(conn, writeMu, reply)
}

func (s *Server) reportSuggestErr(conn net.Conn, writeMu *sync.Mutex, req protocol.Request, err error) {
	text, kind, ok := s.notice(err)
	if !ok {
		s.Log.Debug("suggest", "id", shortID(req.ID), "err", err)
		return
	}
	s.Log.Warn("suggest: non-recoverable", "id", shortID(req.ID), "kind", kind, "err", err)
	s.writeReply(conn, writeMu, protocol.Reply{
		V:          protocol.Version,
		ID:         req.ID,
		Source:     protocol.SourceLLM,
		Notice:     text,
		NoticeKind: kind,
	})
}

func (s *Server) writeReply(conn net.Conn, writeMu *sync.Mutex, reply protocol.Reply) {
	writeMu.Lock()
	defer writeMu.Unlock()
	if err := protocol.Encode(conn, reply); err != nil {
		s.Log.Debug("encode", "err", err)
	}
}
