package cmd

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/internal/version"
)

func TestVersionCmdHasJSONFlag(t *testing.T) {
	flag := versionCmd.Flags().Lookup("json")
	require.NotNil(t, flag)
	assert.Equal(t, "false", flag.DefValue)
}

func TestRunVersionJSONIncludesAllBuildFields(t *testing.T) {
	originalVersionJSON := versionJSON
	t.Cleanup(func() { versionJSON = originalVersionJSON })

	output := captureStdout(t, func() {
		versionJSON = true
		runVersion(nil, nil)
	})

	var info map[string]string
	require.NoError(t, json.Unmarshal([]byte(output), &info))
	assert.Equal(t, version.Version, info["version"])
	assert.Equal(t, version.GitCommit, info["git_commit"])
	assert.Equal(t, version.BuildTime, info["build_time"])
}
