package server

import (
	"context"
	"net/http"
	"time"

	"github.com/ethpandaops/panda/pkg/faucet"
	"github.com/ethpandaops/panda/pkg/operations"
)

// faucetClaimTimeout bounds a single claim so a slow or stuck faucet cannot
// hang the request indefinitely. Argon2id/16MiB mining plus on-chain
// confirmation comfortably fits within this window.
const faucetClaimTimeout = 5 * time.Minute

func (s *service) handleEVMOperation(operationID string, w http.ResponseWriter, r *http.Request) bool {
	switch operationID {
	case "evm.faucet":
		s.handleEVMFaucet(w, r)
	default:
		return false
	}

	return true
}

// handleEVMFaucet mines the network's PoW faucet and claims test ETH to the
// given address, returning the claim transaction hash. It requires an
// authenticated panda session: the faucet dispenses real (testnet) funds, so
// use is gated behind 'panda auth login'.
func (s *service) handleEVMFaucet(w http.ResponseWriter, r *http.Request) {
	if s.credentials == nil || !s.credentials.Status().Authenticated {
		writeAPIError(w, http.StatusUnauthorized,
			"panda auth required to use the faucet: run 'panda auth login' first")

		return
	}

	if s.cartographoorClient == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "network inventory not available")

		return
	}

	req, err := decodeOperationRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())

		return
	}

	network, err := requiredStringArg(req.Args, "network")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())

		return
	}

	address, err := requiredStringArg(req.Args, "address")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())

		return
	}

	net, ok := s.cartographoorClient.GetNetwork(network)
	if !ok {
		writeAPIError(w, http.StatusNotFound, "network not found: "+network+". Use network.list for current ids.")

		return
	}

	if net.ServiceURLs == nil || net.ServiceURLs.Faucet == "" {
		writeAPIError(w, http.StatusNotFound, "no faucet advertised for network "+network)

		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), faucetClaimTimeout)
	defer cancel()

	result, err := faucet.New(net.ServiceURLs.Faucet, nil).Claim(ctx, address)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, "faucet claim failed: "+err.Error())

		return
	}

	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: result,
	})
}
