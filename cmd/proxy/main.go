// Package main provides the standalone proxy server entrypoint.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/ethpandaops/mcp/internal/version"
	"github.com/ethpandaops/mcp/pkg/proxy"
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
	Use:   "ethpandaops-proxy",
	Short: "ethpandaops credential proxy for Ethereum network analytics",
	Long: `A standalone credential proxy that securely proxies requests to ClickHouse,
Prometheus, and Loki backends. This is designed for Kubernetes deployment where
the proxy runs centrally and MCP clients connect using JWTs for authentication.`,
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
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default: $ETHPANDAOPS_PROXY_CONFIG, ~/.config/ethpandaops/proxy-config.yaml, or ./proxy-config.yaml)")
	rootCmd.PersistentFlags().StringVar(&logLevel, "log-level", "info", "log level (debug, info, warn, error)")
}

func runServe(_ *cobra.Command, _ []string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	log.WithField("version", version.Version).Info("Starting ethpandaops credential proxy")

	// Load configuration.
	cfg, err := proxy.LoadServerConfig(cfgFile)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
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

	if err := svc.Stop(shutdownCtx); err != nil {
		return fmt.Errorf("stopping proxy: %w", err)
	}

	log.Info("Proxy stopped gracefully")

	return nil
}
