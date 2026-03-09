// Package cli provides the command-line interface for ethpandaops Ethereum analytics.
package cli

import (
	"os"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

var (
	cfgFile  string
	logLevel string
	log      = logrus.New()
)

var rootCmd = &cobra.Command{
	Use:   "ep",
	Short: "Ethereum network analytics CLI",
	Long: `A CLI for Ethereum network analytics with access to ClickHouse blockchain data,
Prometheus metrics, Loki logs, and sandboxed Python execution.

Run 'ep <command> --help' for details on any command.`,
	PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
		level, err := logrus.ParseLevel(logLevel)
		if err != nil {
			return err
		}

		log.SetLevel(level)
		log.SetFormatter(&logrus.TextFormatter{
			FullTimestamp: true,
		})

		return nil
	},
	SilenceUsage: true,
}

// Execute runs the root command.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default: $ETHPANDAOPS_CONFIG, $EP_CONFIG, ~/.config/ethpandaops/config.yaml, or ./config.yaml)")
	rootCmd.PersistentFlags().StringVar(&logLevel, "log-level", "info", "log level (debug, info, warn, error)")
}
