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
// short for macOS's ~104-byte socket path cap.
const DefaultSocket = "/tmp/zsh-autopilot-metrics.sock"

// Serve listens on socketPath for the zsh client's "shown"/"outcome" events
// (newline-delimited JSON, write-only, no reply), stamping each line with
// user and a derived session_id before handing it to log.Emit.
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
				// parked in Decode, then wait for them; reversed, wg.Wait
				// never returns.
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

// claimSocket refuses to start if a live listener already answers at
// socketPath; if nothing answers, it removes the stale file so net.Listen
// can bind cleanly.
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

// handleConn reads newline-delimited JSON events off conn, decoding each
// loosely into a map (so additive fields survive) before stamping
// user/session_id and forwarding to log.Emit.
func handleConn(ctx context.Context, conn net.Conn, log *Logger, slogger *slog.Logger) {
	defer conn.Close()

	scanner := bufio.NewScanner(conn)
	// Event lines are small JSON objects; the default 64KiB scanner buffer
	// is far more than enough for this dev-only log.
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
