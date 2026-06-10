package cartographoor

import (
	"context"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

// TestLiveFetch exercises the real cartographoor endpoint to confirm panda's
// client pulls and parses live network data correctly. It hits the network, so
// it is skipped under `go test -short`.
//
// Run with: go test ./pkg/cartographoor/ -run TestLiveFetch -v
func TestLiveFetch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live cartographoor fetch in -short mode")
	}

	log := logrus.New()
	log.SetLevel(logrus.DebugLevel)

	client := NewCartographoorClient(log, CartographoorConfig{
		URL:      DefaultCartographoorURL,
		CacheTTL: DefaultCacheTTL,
		Timeout:  DefaultHTTPTimeout,
	})

	ctx, cancel := context.WithTimeout(context.Background(), DefaultHTTPTimeout+5*time.Second)
	defer cancel()

	require.NoError(t, client.Start(ctx), "Start should fetch initial data from the live endpoint")
	defer func() { require.NoError(t, client.Stop()) }()

	all := client.GetAllNetworks()
	active := client.GetActiveNetworks()
	groups := client.GetGroups()

	require.NotEmpty(t, all, "expected at least one network from cartographoor")

	t.Logf("networks: %d total, %d active, %d devnet groups", len(all), len(active), len(groups))
	t.Logf("groups: %v", groups)

	// Print a few sample networks so the data can be eyeballed.
	shown := 0
	for name, n := range active {
		t.Logf("  active: %-28s status=%-8s chainID=%d repo=%s",
			name, n.Status, n.ChainID, n.Repository)

		if shown++; shown >= 5 {
			break
		}
	}
}
