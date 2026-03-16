// Package main provides the standalone proxy server entrypoint.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/ethpandaops/panda/internal/version"
	"github.com/ethpandaops/panda/pkg/proxy"
)

var (
	cfgFile  string
	logLevel string
	log      = logrus.New()
)

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:   "panda-proxy",
	Short: "panda credential proxy for Ethereum network analytics",
	Long: `A standalone credential proxy that securely proxies requests to ClickHouse,
Prometheus, and Loki backends. This is designed for centralized deployment where
the proxy holds upstream credentials and can optionally issue proxy-scoped tokens
after GitHub authentication.`,
	PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
		level, err := logrus.ParseLevel(logLevel)
		if err != nil {
			return err
		}

		log.SetLevel(level)
		log.SetFormatter(&logrus.JSONFormatter{})

		return nil
	},
	RunE: runServe,
}

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default: $PANDA_PROXY_CONFIG, ~/.config/panda/proxy-config.yaml, or ./proxy-config.yaml)")
	rootCmd.PersistentFlags().StringVar(&logLevel, "log-level", "info", "log level (debug, info, warn, error)")
}

func runServe(_ *cobra.Command, _ []string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	log.WithField("version", version.Version).Info("Starting panda credential proxy")

	// Load configuration.
	cfg, err := proxy.LoadServerConfig(cfgFile)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	// Start metrics server if enabled.
	var metricsServer *http.Server

	if cfg.Metrics.Enabled {
		addr := cfg.Metrics.ListenAddr

		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.Handler())

		metricsServer = &http.Server{
			Addr:              addr,
			Handler:           mux,
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       30 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       60 * time.Second,
		}

		go func() {
			log.WithField("addr", addr).Info("Starting metrics server")

			if err := metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.WithError(err).Error("Metrics server error")
			}
		}()
	}

	// Create the proxy server.
	svc, err := proxy.NewServer(log, *cfg)
	if err != nil {
		return fmt.Errorf("creating proxy: %w", err)
	}

	// Handle graceful shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		log.WithField("signal", sig).Info("Received shutdown signal")
		cancel()
	}()

	// Start the proxy.
	if err := svc.Start(ctx); err != nil {
		return fmt.Errorf("starting proxy: %w", err)
	}

	// Wait for context cancellation.
	<-ctx.Done()

	// Graceful shutdown.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	// Stop metrics server.
	if metricsServer != nil {
		if err := metricsServer.Shutdown(shutdownCtx); err != nil {
			log.WithError(err).Warn("Error stopping metrics server")
		}

		log.Info("Metrics server stopped")
	}

	if err := svc.Stop(shutdownCtx); err != nil {
		return fmt.Errorf("stopping proxy: %w", err)
	}

	log.Info("Proxy stopped gracefully")

	return nil
}
