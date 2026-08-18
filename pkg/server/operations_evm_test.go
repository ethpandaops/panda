package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	authclient "github.com/ethpandaops/panda/pkg/auth/client"
	"github.com/ethpandaops/panda/pkg/faucet"
)

// authedService returns a network-operation service whose credential controller
// holds a valid (unexpired) token, so the auth gate lets the request through.
func authedService(t *testing.T) *service {
	t.Helper()

	svc := newNetworkOperationService()

	ctrl, path := newTestController(t, &fakeAuthClient{})
	seedTokens(t, path, &authclient.Tokens{AccessToken: "at", ExpiresAt: time.Now().Add(time.Hour)})
	svc.credentials = ctrl

	return svc
}

func TestFaucetRequiresAuth(t *testing.T) {
	args := map[string]any{"network": "fusaka-devnet-3", "address": "0x1111111111111111111111111111111111111111"}

	t.Run("no credential controller", func(t *testing.T) {
		svc := newNetworkOperationService() // credentials is nil
		rec := httptest.NewRecorder()

		require.True(t, svc.handleEVMOperation("evm.faucet", rec, newNetworkOpRequest(t, args)))
		require.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	t.Run("controller present but not logged in", func(t *testing.T) {
		svc := newNetworkOperationService()
		ctrl, _ := newTestController(t, &fakeAuthClient{}) // no tokens seeded
		svc.credentials = ctrl
		require.False(t, ctrl.Status().Authenticated)

		rec := httptest.NewRecorder()
		require.True(t, svc.handleEVMOperation("evm.faucet", rec, newNetworkOpRequest(t, args)))
		require.Equal(t, http.StatusUnauthorized, rec.Code)
	})
}

func TestFaucetAuthenticatedResolution(t *testing.T) {
	t.Run("unknown network", func(t *testing.T) {
		svc := authedService(t)
		rec := httptest.NewRecorder()

		args := map[string]any{"network": "does-not-exist", "address": "0x1111111111111111111111111111111111111111"}
		require.True(t, svc.handleEVMOperation("evm.faucet", rec, newNetworkOpRequest(t, args)))
		require.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("missing address arg", func(t *testing.T) {
		svc := authedService(t)
		rec := httptest.NewRecorder()

		args := map[string]any{"network": "fusaka-devnet-3"}
		require.True(t, svc.handleEVMOperation("evm.faucet", rec, newNetworkOpRequest(t, args)))
		require.Equal(t, http.StatusBadRequest, rec.Code)
	})
}

// The faucet reports a claim confirmed once it has broadcast the transaction,
// which can precede inclusion — evm.faucet therefore waits for the receipt and
// reports the block. These cover how that receipt is interpreted.
func TestApplyFaucetReceipt(t *testing.T) {
	const hash = "0xf6a59c5d523cd13bcf66ead42c3eacb6a346130d4a119787393fd6a1cd0817d4"

	t.Run("mined receipt confirms and records the block", func(t *testing.T) {
		result := &faucet.Result{ClaimHash: hash}

		require.NoError(t, applyFaucetReceipt(result, map[string]any{
			"status":      "0x1",
			"blockNumber": "0x36ddf",
		}))
		require.True(t, result.Confirmed)
		require.Equal(t, uint64(224735), result.BlockNumber)
	})

	t.Run("pre-Byzantium receipt without status still confirms", func(t *testing.T) {
		result := &faucet.Result{ClaimHash: hash}

		require.NoError(t, applyFaucetReceipt(result, map[string]any{"blockNumber": "0x1"}))
		require.True(t, result.Confirmed)
		require.Equal(t, uint64(1), result.BlockNumber)
	})

	t.Run("reverted receipt is an error", func(t *testing.T) {
		result := &faucet.Result{ClaimHash: hash}

		err := applyFaucetReceipt(result, map[string]any{"status": "0x0", "blockNumber": "0x36ddf"})
		require.ErrorContains(t, err, "reverted on-chain")
		require.False(t, result.Confirmed)
	})

	t.Run("receipt without a block number is an error", func(t *testing.T) {
		result := &faucet.Result{ClaimHash: hash}

		require.ErrorContains(t, applyFaucetReceipt(result, map[string]any{"status": "0x1"}),
			"no block number")
		require.False(t, result.Confirmed)
	})

	t.Run("unparseable block number is an error", func(t *testing.T) {
		result := &faucet.Result{ClaimHash: hash}

		require.ErrorContains(t, applyFaucetReceipt(result, map[string]any{
			"status":      "0x1",
			"blockNumber": "0xzz",
		}), "unparseable block number")
		require.False(t, result.Confirmed)
	})
}
