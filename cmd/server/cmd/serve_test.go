package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServeCmdHasExpectedFlags(t *testing.T) {
	flag := serveCmd.Flags().Lookup("transport")
	require.NotNil(t, flag)
	assert.Equal(t, "t", flag.Shorthand)
	assert.Equal(t, "", flag.DefValue)

	flag = serveCmd.Flags().Lookup("port")
	require.NotNil(t, flag)
	assert.Equal(t, "p", flag.Shorthand)
	assert.Equal(t, "0", flag.DefValue)
}

func TestRunServeReturnsObservabilityStartupError(t *testing.T) {
	originalCfgFile := cfgFile
	originalTransport := transport
	originalPort := port

	t.Cleanup(func() {
		cfgFile = originalCfgFile
		transport = originalTransport
		port = originalPort
	})

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(
		"observability:\n  metrics_enabled: true\n  metrics_port: -1\n"+
			"sandbox:\n  backend: bogus\n  image: sandbox:test\n"+
			"proxy:\n  url: http://proxy.example\n",
	), 0o600))

	cfgFile = configPath
	transport = ""
	port = 0

	err := runServe(nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "starting observability")
	assert.Contains(t, err.Error(), "failed to listen")
}
