package cli

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/internal/version"
)

func TestVersionCommandHasJSONFlag(t *testing.T) {
	flag := versionCmd.Flags().Lookup("json")
	require.NotNil(t, flag)
	assert.Equal(t, "false", flag.DefValue)
}

func TestVersionCommandRunOutputsJSONWhenRequested(t *testing.T) {
	previousVersionJSON := versionJSON
	t.Cleanup(func() { versionJSON = previousVersionJSON })

	stdout, _ := captureOutput(t, func() {
		versionJSON = true
		versionCmd.Run(versionCmd, nil)
	})

	var payload map[string]string
	require.NoError(t, json.Unmarshal([]byte(stdout), &payload))
	assert.Equal(t, version.Version, payload["version"])
	assert.Equal(t, version.GitCommit, payload["git_commit"])
	assert.Equal(t, version.BuildTime, payload["build_time"])
}
