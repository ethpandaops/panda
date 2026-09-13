package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildoorSlotResolverParsesAbsoluteSlots(t *testing.T) {
	// Absolute specs never touch the instance overview, so nil cmd is safe.
	resolve := buildoorSlotResolver(nil, "testnet", "instance")

	value, err := resolve("1234")
	require.NoError(t, err)
	assert.Equal(t, uint64(1234), value)

	_, err = resolve("abc")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected a slot number or +N")

	_, err = resolve("-5")
	require.Error(t, err)
}

func TestBuildoorCommandsAreRegistered(t *testing.T) {
	for _, name := range []string{
		"networks", "instances", "overview", "plan", "results",
		"transform", "test-transform", "plan-update",
	} {
		assert.True(t, hasSubcommand(buildoorCmd, name), "missing subcommand %q", name)
	}
}
