package cli

import (
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRootCommandConfiguresLoggerAndMetadata(t *testing.T) {
	originalLog := log
	originalLogLevel := logLevel
	t.Cleanup(func() {
		log = originalLog
		logLevel = originalLogLevel
	})

	log = logrus.New()
	logLevel = "debug"

	require.NoError(t, rootCmd.PersistentPreRunE(rootCmd, nil))
	assert.Equal(t, logrus.DebugLevel, log.GetLevel())
	assert.True(t, rootCmd.SilenceUsage)
	assert.Equal(t, "panda", rootCmd.Use)

	logLevel = "definitely-invalid"
	require.Error(t, rootCmd.PersistentPreRunE(rootCmd, nil))
}

func TestExecuteRunsCurrentRootCommand(t *testing.T) {
	originalRootCmd := rootCmd
	t.Cleanup(func() { rootCmd = originalRootCmd })

	executed := false
	rootCmd = &cobra.Command{
		Use: "panda-test",
		Run: func(_ *cobra.Command, _ []string) {
			executed = true
		},
	}
	rootCmd.SetArgs([]string{})

	Execute()
	assert.True(t, executed)
}
