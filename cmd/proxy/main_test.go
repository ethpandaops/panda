package main

import (
	"bytes"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRootCmdPersistentPreRunSetsLogger(t *testing.T) {
	prevLog := log
	prevLevel := logLevel
	t.Cleanup(func() {
		log = prevLog
		logLevel = prevLevel
	})

	log = logrus.New()
	logLevel = "debug"

	require.NoError(t, rootCmd.PersistentPreRunE(rootCmd, nil))
	assert.Equal(t, logrus.DebugLevel, log.GetLevel())
	_, ok := log.Formatter.(*logrus.JSONFormatter)
	assert.True(t, ok)
}

func TestRootCmdPersistentPreRunRejectsInvalidLevel(t *testing.T) {
	prevLevel := logLevel
	t.Cleanup(func() { logLevel = prevLevel })

	logLevel = "definitely-not-a-level"
	err := rootCmd.PersistentPreRunE(rootCmd, nil)
	require.Error(t, err)
}

func TestRunServeReturnsConfigLoadError(t *testing.T) {
	prevCfg := cfgFile
	t.Cleanup(func() { cfgFile = prevCfg })

	cfgFile = "/tmp/does-not-exist-proxy-config.yaml"

	err := runServe(nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loading config")
}

func TestRootCommandShape(t *testing.T) {
	assert.Equal(t, "panda-proxy", rootCmd.Use)
	assert.NotNil(t, rootCmd.RunE)
	assert.NotNil(t, rootCmd.PersistentFlags().Lookup("config"))
	assert.NotNil(t, rootCmd.PersistentFlags().Lookup("log-level"))

	var help bytes.Buffer
	rootCmd.SetOut(&help)
	rootCmd.SetErr(&help)
	rootCmd.SetArgs([]string{"--help"})
	err := rootCmd.Execute()
	require.NoError(t, err)
	assert.Contains(t, help.String(), "standalone credential proxy")
}
