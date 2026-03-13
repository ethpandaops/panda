package cmd

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/internal/version"
)

func TestRootCmdPersistentPreRunE(t *testing.T) {
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

	formatter, ok := log.Formatter.(*logrus.TextFormatter)
	require.True(t, ok)
	assert.True(t, formatter.FullTimestamp)

	logLevel = "definitely-invalid"
	require.Error(t, rootCmd.PersistentPreRunE(rootCmd, nil))
}

func TestRunServeReturnsLoadErrorForMissingConfig(t *testing.T) {
	originalCfgFile := cfgFile
	originalPort := port

	t.Cleanup(func() {
		cfgFile = originalCfgFile
		port = originalPort
	})

	cfgFile = filepath.Join(t.TempDir(), "missing.yaml")
	port = 0

	err := runServe(nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loading config")
}

func TestRunServeBuildsConfigAndAppliesOverridesBeforeSandboxFailure(t *testing.T) {
	originalCfgFile := cfgFile
	originalPort := port

	t.Cleanup(func() {
		cfgFile = originalCfgFile
		port = originalPort
	})

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configYAML := `
server:
  transport: stdio
  port: 2480
sandbox:
  backend: bogus
  image: sandbox:test
proxy:
  url: http://proxy.example
`
	require.NoError(t, os.WriteFile(configPath, []byte(configYAML), 0o600))

	cfgFile = configPath
	port = 4319

	err := runServe(nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "building server")
	assert.Contains(t, err.Error(), "unsupported sandbox backend: bogus")
}

func TestRunVersionOutputsTextAndJSONRoot(t *testing.T) {
	originalVersionJSON := versionJSON
	t.Cleanup(func() { versionJSON = originalVersionJSON })

	textOutput := captureRootStdout(t, func() {
		versionJSON = false
		runVersion(nil, nil)
	})
	assert.Contains(t, textOutput, "panda-server version "+version.Version)

	jsonOutput := captureRootStdout(t, func() {
		versionJSON = true
		runVersion(nil, nil)
	})
	assert.Contains(t, jsonOutput, `"version": "`)
	assert.Contains(t, jsonOutput, version.Version)
}

func captureRootStdout(t *testing.T, fn func()) string {
	t.Helper()

	originalStdout := os.Stdout
	reader, writer, err := os.Pipe()
	require.NoError(t, err)

	os.Stdout = writer
	t.Cleanup(func() { os.Stdout = originalStdout })

	fn()

	require.NoError(t, writer.Close())
	data, err := io.ReadAll(reader)
	require.NoError(t, err)

	os.Stdout = originalStdout

	return strings.TrimSpace(string(data))
}
