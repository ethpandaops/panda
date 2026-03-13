package cartographoor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethpandaops/cartographoor/pkg/discovery"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewCartographoorClientAppliesDefaults(t *testing.T) {
	client := NewCartographoorClient(logrus.New(), CartographoorConfig{})

	impl, ok := client.(*cartographoorClient)
	require.True(t, ok)

	assert.Equal(t, DefaultCartographoorURL, impl.cfg.URL)
	assert.Equal(t, DefaultCacheTTL, impl.cfg.CacheTTL)
	assert.Equal(t, DefaultHTTPTimeout, impl.cfg.Timeout)
	assert.NotNil(t, impl.client)
	assert.Empty(t, impl.networks)
	assert.Empty(t, impl.groups)
}

func TestCartographoorClientRefreshAndGetters(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		require.NoError(t, json.NewEncoder(w).Encode(discovery.Result{
			Networks: map[string]discovery.Network{
				"fusaka-devnet-1": {
					Name:       "fusaka-devnet-1",
					Repository: "ethpandaops/fusaka-devnets",
					Status:     "active",
				},
				"fusaka-devnet-2": {
					Name:       "fusaka-devnet-2",
					Repository: "ethpandaops/fusaka-devnets",
					Status:     "inactive",
				},
				"mainnet": {
					Name:       "mainnet",
					Repository: "ethpandaops/mainnet",
					Status:     "active",
				},
			},
		}))
	}))
	defer server.Close()

	client := NewCartographoorClient(logrus.New(), CartographoorConfig{
		URL:      server.URL,
		CacheTTL: time.Hour,
		Timeout:  time.Second,
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	require.NoError(t, client.Start(ctx))
	t.Cleanup(func() {
		require.NoError(t, client.Stop())
	})

	allNetworks := client.GetAllNetworks()
	require.Len(t, allNetworks, 3)

	allNetworks["shadow"] = discovery.Network{Name: "shadow"}
	refetchedNetworks := client.GetAllNetworks()
	_, exists := refetchedNetworks["shadow"]
	assert.False(t, exists)

	active := client.GetActiveNetworks()
	assert.Len(t, active, 2)
	assert.Contains(t, active, "mainnet")
	assert.Contains(t, active, "fusaka-devnet-1")

	network, ok := client.GetNetwork("mainnet")
	require.True(t, ok)
	assert.Equal(t, "mainnet", network.Name)

	group, ok := client.GetGroup("fusaka")
	require.True(t, ok)
	assert.Len(t, group, 2)
	assert.Contains(t, group, "fusaka-devnet-1")
	assert.Contains(t, group, "fusaka-devnet-2")

	missingGroup, ok := client.GetGroup("missing")
	assert.False(t, ok)
	assert.Nil(t, missingGroup)

	assert.Equal(t, []string{"fusaka"}, client.GetGroups())
	assert.True(t, client.IsDevnet(networkFromRepo("ethpandaops/fusaka-devnets")))
	assert.False(t, client.IsDevnet(network))
	assert.Equal(t, []string{"xatu-experimental", "xatu-cbt"}, client.GetClusters(networkFromRepo("ethpandaops/fusaka-devnets")))
	assert.Equal(t, []string{"xatu", "xatu-cbt"}, client.GetClusters(network))
}

func TestCartographoorClientBackgroundRefresh(t *testing.T) {
	var requests atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count := requests.Add(1)
		result := discovery.Result{
			Networks: map[string]discovery.Network{
				"mainnet": {
					Name:       "mainnet",
					Repository: "ethpandaops/mainnet",
					Status:     "active",
				},
			},
		}

		if count >= 2 {
			result.Networks["fusaka-devnet-1"] = discovery.Network{
				Name:       "fusaka-devnet-1",
				Repository: "ethpandaops/fusaka-devnets",
				Status:     "active",
			}
		}

		require.NoError(t, json.NewEncoder(w).Encode(result))
	}))
	defer server.Close()

	client := NewCartographoorClient(logrus.New(), CartographoorConfig{
		URL:      server.URL,
		CacheTTL: 20 * time.Millisecond,
		Timeout:  time.Second,
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	require.NoError(t, client.Start(ctx))
	t.Cleanup(func() {
		require.NoError(t, client.Stop())
	})

	require.Eventually(t, func() bool {
		_, ok := client.GetNetwork("fusaka-devnet-1")
		return ok
	}, time.Second, 20*time.Millisecond)

	assert.GreaterOrEqual(t, requests.Load(), int32(2))
}

func TestCartographoorClientRefreshErrors(t *testing.T) {
	t.Run("unexpected status", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "boom", http.StatusBadGateway)
		}))
		defer server.Close()

		client := NewCartographoorClient(logrus.New(), CartographoorConfig{
			URL:     server.URL,
			Timeout: time.Second,
		}).(*cartographoorClient)

		err := client.refresh(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected status code: 502")
	})

	t.Run("invalid payload", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, err := w.Write([]byte("{"))
			require.NoError(t, err)
		}))
		defer server.Close()

		client := NewCartographoorClient(logrus.New(), CartographoorConfig{
			URL:     server.URL,
			Timeout: time.Second,
		}).(*cartographoorClient)

		err := client.refresh(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "decoding response")
	})
}

func networkFromRepo(repository string) discovery.Network {
	return discovery.Network{
		Name:       repository,
		Repository: repository,
		Status:     "active",
	}
}
