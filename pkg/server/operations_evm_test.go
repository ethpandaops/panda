package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	authclient "github.com/ethpandaops/panda/pkg/auth/client"
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
