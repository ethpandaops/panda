package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// machineOutputFor mirrors the PersistentPreRunE decision so the rule can be
// exercised without executing a command.
func machineOutputFor(t *testing.T, format, commandName string) bool {
	t.Helper()

	previous := outputFormat
	t.Cleanup(func() { outputFormat = previous })

	outputFormat = format

	return isJSON() || alwaysJSONCommands[commandName]
}

func TestMachineOutputCoversEveryJSONSurface(t *testing.T) {
	// The failure this guards is a JSON consumer merging stderr into the pipe:
	// it breaks on any command rendering JSON, not just the always-JSON ones.
	assert.True(t, machineOutputFor(t, "json", "query"),
		"any command in JSON output mode is a machine surface")
	assert.True(t, machineOutputFor(t, "text", "query-raw"),
		"query-raw emits JSON regardless of --output")
	assert.False(t, machineOutputFor(t, "text", "query"),
		"human text output keeps cobra's stderr error line")
}

func TestQueryRawStaysAnAlwaysJSONSurface(t *testing.T) {
	// Guards the contract its help text advertises.
	require.True(t, alwaysJSONCommands[clickhouseQueryRawCmd.Name()])
}
