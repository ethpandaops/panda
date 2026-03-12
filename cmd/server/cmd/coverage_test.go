package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
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
	logLevel = "warn"

	require.NoError(t, rootCmd.PersistentPreRunE(rootCmd, nil))
	assert.Equal(t, logrus.WarnLevel, log.GetLevel())
	_, ok := log.Formatter.(*logrus.TextFormatter)
	assert.True(t, ok)
}

func TestRootCmdPersistentPreRunRejectsInvalidLevel(t *testing.T) {
	prevLevel := logLevel
	t.Cleanup(func() { logLevel = prevLevel })

	logLevel = "nope"
	err := rootCmd.PersistentPreRunE(rootCmd, nil)
	require.Error(t, err)
}

func TestRunServeReturnsConfigLoadError(t *testing.T) {
	prevCfg := cfgFile
	prevPort := port
	t.Cleanup(func() {
		cfgFile = prevCfg
		port = prevPort
	})

	cfgFile = "/tmp/does-not-exist-server-config.yaml"
	port = 2480

	err := runServe(nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loading config")
}

func TestRunVersionOutputsTextAndJSON(t *testing.T) {
	prevJSON := versionJSON
	t.Cleanup(func() { versionJSON = prevJSON })

	versionJSON = false
	textOutput := captureStdout(t, func() {
		runVersion(nil, nil)
	})
	assert.Contains(t, textOutput, "panda-server version")

	versionJSON = true
	jsonOutput := captureStdout(t, func() {
		runVersion(nil, nil)
	})

	var payload map[string]string
	require.NoError(t, json.Unmarshal([]byte(jsonOutput), &payload))
	assert.Contains(t, payload, "version")
	assert.Contains(t, payload, "git_commit")
	assert.Contains(t, payload, "build_time")
}

func TestCommandRegistration(t *testing.T) {
	assert.NotNil(t, rootCmd.PersistentFlags().Lookup("config"))
	assert.NotNil(t, rootCmd.PersistentFlags().Lookup("log-level"))
	assert.NotNil(t, serveCmd.Flags().Lookup("port"))
	assert.Contains(t, rootCmd.Commands(), serveCmd)
	assert.Contains(t, rootCmd.Commands(), versionCmd)

	var help bytes.Buffer
	rootCmd.SetOut(&help)
	rootCmd.SetErr(&help)
	rootCmd.SetArgs([]string{"--help"})
	err := rootCmd.Execute()
	require.NoError(t, err)
	assert.Contains(t, help.String(), "panda server")
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	origStdout := os.Stdout
	reader, writer, err := os.Pipe()
	require.NoError(t, err)

	os.Stdout = writer
	defer func() {
		os.Stdout = origStdout
	}()

	fn()

	require.NoError(t, writer.Close())
	data, err := io.ReadAll(reader)
	require.NoError(t, err)

	return string(bytes.TrimSpace(data))
}
