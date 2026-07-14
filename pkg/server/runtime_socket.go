package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// serveRuntimeSocket also serves the runtime API on a unix socket (the direct
// backend's netns has no TCP route); callers are still gated by the bearer token.
func (s *service) serveRuntimeSocket(handler http.Handler) error {
	if err := os.MkdirAll(filepath.Dir(s.runtimeSocketPath), 0o755); err != nil {
		return fmt.Errorf("creating runtime socket dir: %w", err)
	}

	// Remove a leftover socket from a crashed run (only this server uses the path).
	if err := os.Remove(s.runtimeSocketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing stale runtime socket: %w", err)
	}

	listener, err := net.Listen("unix", s.runtimeSocketPath)
	if err != nil {
		return fmt.Errorf("listening on runtime socket %s: %w", s.runtimeSocketPath, err)
	}

	// The sandbox connects as the exec uid (not the server uid); connect(2) needs
	// the write bit, so 0666. Access is still token-gated.
	if err := os.Chmod(s.runtimeSocketPath, 0o666); err != nil {
		_ = listener.Close()

		return fmt.Errorf("chmod runtime socket: %w", err)
	}

	s.runtimeSocketServer = &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	s.log.WithField("socket", s.runtimeSocketPath).Info("Serving runtime API on unix socket for the direct sandbox backend")

	go func() {
		if err := s.runtimeSocketServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.WithError(err).Error("Runtime socket server stopped")
		}
	}()

	return nil
}

// stopRuntimeSocket shuts the runtime socket server down and removes the socket
// file so a subsequent start is not blocked by a stale path.
func (s *service) stopRuntimeSocket() {
	if s.runtimeSocketServer != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_ = s.runtimeSocketServer.Shutdown(shutdownCtx)
	}

	_ = os.Remove(s.runtimeSocketPath)
}
