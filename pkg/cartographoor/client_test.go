package cartographoor

import (
	"testing"

	"github.com/ethpandaops/cartographoor/pkg/discovery"
	"github.com/stretchr/testify/assert"
)

func TestBuildGroups(t *testing.T) {
	networks := map[string]discovery.Network{
		"fusaka-devnet-1": {Name: "fusaka-devnet-1", Repository: "ethpandaops/fusaka-devnets"},
		"fusaka-devnet-2": {Name: "fusaka-devnet-2", Repository: "ethpandaops/fusaka-devnets"},
		"pectra-devnet-5": {Name: "pectra-devnet-5", Repository: "ethpandaops/pectra-devnets"},
		"mainnet":         {Name: "mainnet", Repository: "ethereum/mainnet"},
		"no-repo":         {Name: "no-repo"},
	}

	groups := buildGroups(networks)

	assert.Len(t, groups, 2)
	// Networks within a group are sorted for stable output.
	assert.Equal(t, []string{"fusaka-devnet-1", "fusaka-devnet-2"}, groups["fusaka"])
	assert.Equal(t, []string{"pectra-devnet-5"}, groups["pectra"])
	// Non-devnet repositories are not grouped.
	assert.NotContains(t, groups, "mainnet")
}

func TestClientAccessors(t *testing.T) {
	c := &cartographoorClient{
		networks: map[string]discovery.Network{
			"active-net":   {Name: "active-net", Status: "active", Repository: "ethpandaops/fusaka-devnets"},
			"inactive-net": {Name: "inactive-net", Status: "inactive", Repository: "ethpandaops/fusaka-devnets"},
			"mainnet":      {Name: "mainnet", Status: "active", Repository: "ethereum/mainnet"},
		},
		groups: map[string][]string{
			"fusaka": {"active-net", "inactive-net"},
		},
	}

	t.Run("GetAllNetworks returns a copy of every network", func(t *testing.T) {
		all := c.GetAllNetworks()
		assert.Len(t, all, 3)

		// Mutating the returned map must not affect internal state.
		delete(all, "mainnet")
		assert.Len(t, c.GetAllNetworks(), 3)
	})

	t.Run("GetActiveNetworks filters by status", func(t *testing.T) {
		active := c.GetActiveNetworks()
		assert.Len(t, active, 2)
		assert.Contains(t, active, "active-net")
		assert.Contains(t, active, "mainnet")
	})

	t.Run("GetNetwork resolves by name", func(t *testing.T) {
		net, ok := c.GetNetwork("mainnet")
		assert.True(t, ok)
		assert.Equal(t, "mainnet", net.Name)

		_, ok = c.GetNetwork("missing")
		assert.False(t, ok)
	})

	t.Run("GetGroup returns networks in a devnet group", func(t *testing.T) {
		group, ok := c.GetGroup("fusaka")
		assert.True(t, ok)
		assert.Len(t, group, 2)
		assert.Contains(t, group, "active-net")

		_, ok = c.GetGroup("missing")
		assert.False(t, ok)
	})

	t.Run("GetGroups returns sorted group names", func(t *testing.T) {
		assert.Equal(t, []string{"fusaka"}, c.GetGroups())
	})

	t.Run("GetActiveGroups filters inactive networks", func(t *testing.T) {
		assert.Equal(t, map[string][]string{
			"fusaka": {"active-net"},
		}, c.GetActiveGroups())
	})
}
