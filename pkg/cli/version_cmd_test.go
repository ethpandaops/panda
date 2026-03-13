package cli

import (
	"encoding/json"
	"testing"

	"github.com/ethpandaops/panda/internal/version"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVersionCommandOutputsTextAndJSON(t *testing.T) {
	originalVersionJSON := versionJSON
	t.Cleanup(func() { versionJSON = originalVersionJSON })

	stdout, _ := captureOutput(t, func() {
		versionJSON = false
		require.NoError(t, versionCmd.RunE(versionCmd, nil))
	})
	assert.Contains(t, stdout, "panda version "+version.Version)

	stdout, _ = captureOutput(t, func() {
		versionJSON = true
		require.NoError(t, versionCmd.RunE(versionCmd, nil))
	})

	var payload map[string]string
	require.NoError(t, json.Unmarshal([]byte(stdout), &payload))
	assert.Equal(t, version.Version, payload["version"])
	assert.Equal(t, version.GitCommit, payload["git_commit"])
	assert.Equal(t, version.BuildTime, payload["build_time"])
}
