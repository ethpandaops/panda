package cli

import (
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRootCommandPersistentPreRunSetsLogger(t *testing.T) {
	originalLog := log
	originalLogLevel := logLevel
	t.Cleanup(func() {
		log = originalLog
		logLevel = originalLogLevel
	})

	log = logrus.New()
	logLevel = "warn"

	require.NoError(t, rootCmd.PersistentPreRunE(rootCmd, nil))
	assert.Equal(t, logrus.WarnLevel, log.GetLevel())
	assert.True(t, rootCmd.SilenceUsage)
}

func TestRootCommandRegistersPersistentFlags(t *testing.T) {
	configFlag := rootCmd.PersistentFlags().Lookup("config")
	require.NotNil(t, configFlag)
	assert.Contains(t, configFlag.Usage, "config file")

	logLevelFlag := rootCmd.PersistentFlags().Lookup("log-level")
	require.NotNil(t, logLevelFlag)
	assert.Equal(t, "info", logLevelFlag.DefValue)
}
