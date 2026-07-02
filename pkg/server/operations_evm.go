package server

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"regexp"
	"time"

	"github.com/ethpandaops/panda/pkg/faucet"
	"github.com/ethpandaops/panda/pkg/operations"
)

// faucetClaimTimeout bounds a single claim so a slow or stuck faucet cannot hang
// the request. Argon2id/16MiB mining plus on-chain confirmation fits comfortably.
const faucetClaimTimeout = 5 * time.Minute

// faucetNetworkPattern guards the network segment used to build the proxy path.
var faucetNetworkPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*[a-z0-9]$|^[a-z0-9]$`)

func (s *service) handleEVMOperation(operationID string, w http.ResponseWriter, r *http.Request) bool {
	switch operationID {
	case "evm.faucet":
		s.handleEVMFaucet(w, r)
	default:
		return false
	}

	return true
}

// handleEVMFaucet mines the network's PoW faucet and claims test ETH to address,
// returning the claim transaction hash. The faucet is reachable only through the
// panda proxy (which authenticates the request), so it has no public surface —
// the local auth check below is only a friendly fast-fail, not the boundary.
func (s *service) handleEVMFaucet(w http.ResponseWriter, r *http.Request) {
	if s.credentials == nil || !s.credentials.Status().Authenticated {
		writeAPIError(w, http.StatusUnauthorized,
			"panda auth required to use the faucet: run 'panda auth login' first")

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

	if !faucetNetworkPattern.MatchString(network) {
		writeAPIError(w, http.StatusBadRequest, "invalid network name")

		return
	}

	address, err := requiredStringArg(req.Args, "address")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())

		return
	}

	if s.cartographoorClient != nil {
		if _, ok := s.cartographoorClient.GetNetwork(network); !ok {
			writeAPIError(w, http.StatusNotFound, "network not found: "+network+". Use network.list for current ids.")

			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), faucetClaimTimeout)
	defer cancel()

	client := faucet.NewWithTransport(&proxyFaucetTransport{s: s, network: network})

	result, err := client.Claim(ctx, address)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, "faucet claim failed: "+err.Error())

		return
	}

	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: result,
	})
}

// proxyFaucetTransport routes faucet REST calls through the proxy's
// /faucet/{network}/ passthrough. The proxy authenticates the caller's bearer
// token and attaches the faucet credential, which never leaves the proxy.
type proxyFaucetTransport struct {
	s       *service
	network string
}

func (p *proxyFaucetTransport) Do(ctx context.Context, method, path string, body []byte) ([]byte, int, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	headers := http.Header{}
	if body != nil {
		headers.Set("Content-Type", "application/json")
	}

	data, status, _, err := p.s.proxyRequest(ctx, method, "/faucet/"+p.network+path, reader, headers)

	return data, status, err
}
