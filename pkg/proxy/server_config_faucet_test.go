package proxy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestToFaucetHandlerConfig covers the credential fallback chain: dedicated
// faucet block wins, ethnode credential is the fallback, neither disables the
// route.
func TestToFaucetHandlerConfig(t *testing.T) {
	ethnode := &EthNodeInstanceConfig{Username: "srv", Password: "srv-pass"}

	t.Run("dedicated faucet credential wins over ethnode", func(t *testing.T) {
		cfg := &ServerConfig{
			EthNode: ethnode,
			Faucet:  &FaucetInstanceConfig{Username: "faucet", Password: "faucet-pass"},
		}

		fc := cfg.ToFaucetHandlerConfig()
		require.NotNil(t, fc)
		assert.Equal(t, "faucet", fc.Username)
		assert.Equal(t, "faucet-pass", fc.Password)
	})

	t.Run("falls back to ethnode credential", func(t *testing.T) {
		cfg := &ServerConfig{EthNode: ethnode}

		fc := cfg.ToFaucetHandlerConfig()
		require.NotNil(t, fc)
		assert.Equal(t, "srv", fc.Username)
		assert.Equal(t, "srv-pass", fc.Password)
	})

	t.Run("empty faucet username falls back to ethnode", func(t *testing.T) {
		cfg := &ServerConfig{
			EthNode: ethnode,
			Faucet:  &FaucetInstanceConfig{},
		}

		fc := cfg.ToFaucetHandlerConfig()
		require.NotNil(t, fc)
		assert.Equal(t, "srv", fc.Username)
	})

	t.Run("no credentials disables the route", func(t *testing.T) {
		assert.Nil(t, (&ServerConfig{}).ToFaucetHandlerConfig())
	})
}
