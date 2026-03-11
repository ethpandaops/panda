// Package observability provides metrics capabilities for ethpandaops-panda.
package observability

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/config"
)

// Service defines the interface for observability services.
type Service interface {
	// Start initializes and starts the metrics HTTP server if enabled.
	Start(ctx context.Context) error
	// Stop gracefully shuts down the metrics server.
	Stop() error
}

// service implements the Service interface.
type service struct {
	log    logrus.FieldLogger
	cfg    config.ObservabilityConfig
	server *http.Server
	mu     sync.Mutex
}

// NewService creates a new observability service.
func NewService(log logrus.FieldLogger, cfg config.ObservabilityConfig) Service {
	return &service{
		log: log.WithField("component", "observability"),
		cfg: cfg,
	}
}

// Start initializes and starts the metrics HTTP server if enabled.
func (s *service) Start(ctx context.Context) error {
	if !s.cfg.MetricsEnabled {
		s.log.Info("Metrics are disabled, skipping metrics server startup")

		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.server != nil {
		return errors.New("metrics server already started")
	}

	addr := fmt.Sprintf(":%d", s.cfg.MetricsPort)

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/health", s.healthHandler)
	mux.HandleFunc("/ready", s.readyHandler)

	s.server = &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", addr, err)
	}

	go func() {
		s.log.WithField("address", addr).Info("Starting metrics server")

		if err := s.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.WithError(err).Error("Metrics server error")
		}
	}()

	return nil
}

// Stop gracefully shuts down the metrics server.
func (s *service) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.server == nil {
		return nil
	}

	s.log.Info("Stopping metrics server")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := s.server.Shutdown(ctx); err != nil {
		return fmt.Errorf("failed to gracefully shutdown metrics server: %w", err)
	}

	s.server = nil

	s.log.Info("Metrics server stopped")

	return nil
}

// healthHandler returns a simple health check response.
func (s *service) healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	_, _ = w.Write([]byte(`{"status":"healthy"}`))
}

// readyHandler returns a readiness check response.
func (s *service) readyHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	_, _ = w.Write([]byte(`{"status":"ready"}`))
}

// Compile-time interface compliance check.
var _ Service = (*service)(nil)
