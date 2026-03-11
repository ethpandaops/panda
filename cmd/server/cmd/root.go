package cmd

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
	Use:   "panda-server",
	Short: "panda server for Ethereum network analytics",
	Long: `A panda server that provides AI assistants with Ethereum network analytics
capabilities including ClickHouse blockchain data, Prometheus metrics, Loki logs,
and sandboxed Python execution.`,
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
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default: $PANDA_CONFIG, ~/.config/panda/config.yaml, or ./config.yaml)")
	rootCmd.PersistentFlags().StringVar(&logLevel, "log-level", "info", "log level (debug, info, warn, error)")
}
