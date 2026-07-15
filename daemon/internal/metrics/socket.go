package metrics

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
)

// DefaultSocket is the default path for the metrics-only Unix socket, kept
// short for macOS's ~104-byte socket path cap (same constraint as
// server.DefaultSocket).
const DefaultSocket = "/tmp/zsh-autopilot-metrics.sock"

// Serve listens on socketPath for the zsh client's "shown" and "outcome"
// events (newline-delimited JSON, write-only from the client's perspective —
// this listener never writes a reply). Each decoded line is stamped with
// user and a derived session_id, then handed to log.Emit. Serve blocks until
// ctx is cancelled, then tears down and returns nil (or a non-nil error if
// the socket cannot be claimed/listened on).
//
// Teardown mirrors internal/server.Server.Run's proven discipline exactly
// (cancel -> close in-flight conns to unblock readers -> wg.Wait -> remove
// socket file); the reverse order deadlocks there and would here too.
func Serve(ctx context.Context, socketPath string, log *Logger, slogger *slog.Logger) error {
	if slogger == nil {
		slogger = slog.Default()
	}

	if err := claimSocket(socketPath, slogger); err != nil {
		return err
	}

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return err
	}
	defer os.Remove(socketPath)

	slogger.Info("metrics: listening", "socket", socketPath)

	var connsMu sync.Mutex
	conns := make(map[net.Conn]struct{})

	// Stop accepting and unblock Accept() when ctx is cancelled.
	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	var wg sync.WaitGroup
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				// Close in-flight connections first to unblock handlers
				// parked in Decode, THEN wait for them. The reverse order
				// deadlocks: Wait never returns while a handler is still
				// blocked reading an open connection.
				connsMu.Lock()
				for c := range conns {
					c.Close()
				}
				connsMu.Unlock()
				wg.Wait()
				os.Remove(socketPath)
				return nil
			default:
				slogger.Error("metrics: accept", "err", err)
				continue
			}
		}

		connsMu.Lock()
		conns[conn] = struct{}{}
		connsMu.Unlock()

		wg.Go(func() {
			defer func() {
				connsMu.Lock()
				delete(conns, conn)
				connsMu.Unlock()
			}()
			handleConn(ctx, conn, log, slogger)
		})
	}
}

// claimSocket mirrors server.claimSocket's stale-socket handling: if a live
// listener already answers at socketPath, refuse to start; if the file is
// stale (nothing answers), remove it so net.Listen can bind cleanly.
func claimSocket(socketPath string, slogger *slog.Logger) error {
	if _, err := os.Stat(socketPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	conn, err := net.Dial("unix", socketPath)
	if err == nil {
		conn.Close()
		slogger.Error("metrics: another instance is already listening", "socket", socketPath)
		return errors.New("metrics: socket " + socketPath + " is already in use")
	}

	slogger.Debug("metrics: removing stale socket", "socket", socketPath)
	return os.Remove(socketPath)
}

// handleConn reads newline-delimited JSON events off conn until EOF, a
// decode error, or ctx cancellation (which closes conn out from under the
// blocked read, same as internal/server's handling). Each event is decoded
// loosely into a map so unknown/additive fields survive untouched (design:
// treat "shown"/"outcome" as passthrough), then user and session_id are
// stamped in before forwarding to log.Emit. No reply is ever written.
func handleConn(ctx context.Context, conn net.Conn, log *Logger, slogger *slog.Logger) {
	defer conn.Close()

	scanner := bufio.NewScanner(conn)
	// Event lines are small JSON objects; the default 64KiB scanner buffer
	// is far more than enough and matches protocol's use of json.Decoder
	// (unbounded) closely enough for this dev-only log.
	for scanner.Scan() {
		if ctx.Err() != nil {
			return
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var ev map[string]any
		if err := json.Unmarshal(line, &ev); err != nil {
			slogger.Debug("metrics: decode", "err", err)
			continue
		}

		ev["user"] = log.User()
		if reqID, ok := ev["request_id"].(string); ok {
			ev["session_id"] = SessionID(reqID)
		}

		log.Emit(ev)
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
		slogger.Debug("metrics: read", "err", err)
	}
}
