package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/ethpandaops/panda/internal/version"
	"github.com/ethpandaops/panda/pkg/config"
	"github.com/ethpandaops/panda/pkg/observability"
	"github.com/ethpandaops/panda/pkg/server"
)

var (
	transport string
	port      int
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the MCP server",
	Long:  `Start the MCP server with the configured transport (stdio, sse, or streamable-http).`,
	RunE:  runServe,
}

func init() {
	rootCmd.AddCommand(serveCmd)

	serveCmd.Flags().StringVarP(&transport, "transport", "t", "", "Transport type (stdio, sse, streamable-http). Overrides config.")
	serveCmd.Flags().IntVarP(&port, "port", "p", 0, "Port number. Overrides config.")
}

func runServe(_ *cobra.Command, _ []string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	log.WithField("version", version.Version).Info("Starting panda server")

	// Load configuration.
	cfg, err := config.Load(cfgFile)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	// Apply CLI overrides.
	if transport != "" {
		cfg.Server.Transport = transport
	}

	if port != 0 {
		cfg.Server.Port = port
	}

	// Start observability service (metrics).
	obsSvc := observability.NewService(log, cfg.Observability)
	if err := obsSvc.Start(ctx); err != nil {
		return fmt.Errorf("starting observability: %w", err)
	}

	defer func() {
		if err := obsSvc.Stop(); err != nil {
			log.WithError(err).Error("Failed to stop observability service")
		}
	}()

	// Build all dependencies.
	builder := server.NewBuilder(log, cfg)

	svc, err := builder.Build(ctx)
	if err != nil {
		return fmt.Errorf("building server: %w", err)
	}

	defer func() {
		if err := svc.Stop(); err != nil {
			log.WithError(err).Error("Failed to stop server")
		}
	}()

	// Handle graceful shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		log.WithField("signal", sig).Info("Received shutdown signal")
		signal.Stop(sigCh)
		cancel()
	}()

	// Start the server.
	if err := svc.Start(ctx); err != nil {
		return fmt.Errorf("server error: %w", err)
	}

	return nil
}
