package server

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"regexp"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/faucet"
	"github.com/ethpandaops/panda/pkg/operations"
)

// faucetClaimTimeout bounds a single claim so a slow or stuck faucet cannot hang
// the request. Larger payouts can require several Argon2id shares.
const faucetClaimTimeout = 12 * time.Minute

// The faucet reports a claim "confirmed" as soon as it has broadcast the
// transaction, which can precede inclusion — so the operation waits for the
// receipt itself before returning.
//
// Each receipt poll batch is deliberately short. Mining already burns most of the caller's
// budget (sandbox.timeout defaults to 60s). Jobs continue polling within
// their own lifetime; synchronous compatibility can return an unconfirmed hash.
const (
	faucetReceiptTimeout      = 30 * time.Second
	faucetReceiptPollInterval = 2 * time.Second
)

// faucetReceiptInstance is the ethnode handler's sentinel for a network's
// load-balanced execution endpoint (rpc.<network>.ethpandaops.io), so the
// receipt wait does not need to know any individual node name.
const faucetReceiptInstance = "lb"

// faucetNetworkPattern guards the network segment used to build the proxy path.
var (
	faucetNetworkPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*[a-z0-9]$|^[a-z0-9]$`)
	faucetAddressPattern = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)
	faucetAmountPattern  = regexp.MustCompile(`^[0-9]+$`)
)

func (s *service) handleEVMOperation(operationID string, w http.ResponseWriter, r *http.Request) bool {
	switch operationID {
	case "evm.faucet":
		s.handleEVMFaucet(w, r)
	case "evm.faucet_start":
		s.handleEVMFaucetStart(w, r)
	case "evm.faucet_status":
		s.handleEVMFaucetStatus(w, r)
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
	network, address, amountWei, _, ok := s.decodeFaucetRequest(w, r)
	if !ok {
		return
	}

	job, status, err := s.faucetJobStore().start(r.Context(), network, address, amountWei, "")
	if err != nil {
		writeAPIError(w, status, err.Error())
		return
	}
	// Compatibility endpoint: only the wait belongs to the HTTP request. The
	// job survives disconnect, and repeated calls recover the same claim.
	ctx, cancel := context.WithTimeout(r.Context(), 600*time.Second)
	defer cancel()
	for {
		if job.State == "failed" {
			writeAPIError(w, http.StatusBadGateway, "faucet job "+job.ID+" failed: "+job.Error)
			return
		}
		if job.ClaimHash != "" {
			result := &faucet.Result{Session: job.Session, Target: job.Address, ClaimHash: job.ClaimHash, AmountWei: job.AmountWei, Confirmed: job.State == "confirmed", BlockNumber: job.BlockNumber}
			writeOperationResponse(s.log, w, http.StatusOK, operations.Response{Kind: operations.ResultKindObject, Data: result, Meta: map[string]any{"job_id": job.ID}})
			return
		}
		if job.Terminal {
			writeAPIError(w, http.StatusBadGateway, "faucet job "+job.ID+" stopped: "+job.Error+"; check original address balance before retrying")
			return
		}
		select {
		case <-ctx.Done():
			writeAPIError(w, http.StatusGatewayTimeout, "faucet wait ended; job "+job.ID+" continues independently; poll evm.faucet_status")
			return
		case <-time.After(faucetReceiptPollInterval):
		}
		job, _ = s.faucetJobStore().get(job.ID)
	}
}

// decodeFaucetRequest validates both synchronous and asynchronous claims.
func (s *service) decodeFaucetRequest(w http.ResponseWriter, r *http.Request) (string, string, *big.Int, map[string]any, bool) {
	if s.credentials == nil || !s.credentials.Status().Authenticated {
		writeAPIError(w, http.StatusUnauthorized,
			"panda auth required to use the faucet: run 'panda auth login' first")

		return "", "", nil, nil, false
	}

	req, err := decodeOperationRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())

		return "", "", nil, nil, false
	}

	network, err := requiredStringArg(req.Args, "network")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())

		return "", "", nil, nil, false
	}

	if !faucetNetworkPattern.MatchString(network) {
		writeAPIError(w, http.StatusBadRequest, "invalid network name")

		return "", "", nil, nil, false
	}

	address, err := requiredStringArg(req.Args, "address")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())

		return "", "", nil, nil, false
	}
	if !faucetAddressPattern.MatchString(address) {
		writeAPIError(w, http.StatusBadRequest, "invalid Ethereum address")
		return "", "", nil, nil, false
	}
	var amountWei *big.Int
	if raw, ok := req.Args["amount_wei"]; ok && raw != nil {
		value, valid := raw.(string)
		if !valid || !faucetAmountPattern.MatchString(value) {
			writeAPIError(w, http.StatusBadRequest, "amount_wei must be a decimal wei string")
			return "", "", nil, nil, false
		}
		amountWei, valid = new(big.Int).SetString(value, 10)
		if !valid || amountWei.Sign() <= 0 {
			writeAPIError(w, http.StatusBadRequest, "amount_wei must be a positive decimal wei string")
			return "", "", nil, nil, false
		}
	}

	if s.cartographoorClient != nil {
		if _, ok := s.cartographoorClient.GetNetwork(network); !ok {
			writeAPIError(w, http.StatusNotFound, "network not found: "+network+". Use network.list for current ids.")

			return "", "", nil, nil, false
		}
	}

	return network, address, amountWei, req.Args, true
}

// awaitFaucetReceipt polls the network's load-balanced execution RPC until the
// claim transaction has a receipt, recording the block it landed in.
//
// The claim is already paid for by the time this runs, so a missing receipt is
// never fatal: a slow chain or an unreachable execution endpoint leaves
// Confirmed false with the hash intact, rather than reporting a funded address
// as a failed claim. Only a receipt saying the transaction reverted is an error.
func (s *service) awaitFaucetReceipt(ctx context.Context, network string, result *faucet.Result) error {
	ctx, cancel := context.WithTimeout(ctx, faucetReceiptTimeout)
	defer cancel()

	var lastErr error

	for {
		raw, _, err := s.ethNodeExecutionRPC(
			ctx, network, faucetReceiptInstance,
			"eth_getTransactionReceipt", []any{result.ClaimHash},
		)
		if err != nil {
			lastErr = err
		} else if receipt, ok := raw.(map[string]any); ok {
			// Anything else (a JSON null) means "not mined yet"; keep polling.
			return applyFaucetReceipt(result, receipt)
		}

		select {
		case <-ctx.Done():
			s.log.WithError(lastErr).WithFields(logrus.Fields{
				"network":    network,
				"claim_hash": result.ClaimHash,
			}).Warn("Faucet claim submitted but not seen on-chain within the receipt wait")

			return nil
		case <-time.After(faucetReceiptPollInterval):
		}
	}
}

// applyFaucetReceipt records a mined claim on result, or reports a reverted one.
func applyFaucetReceipt(result *faucet.Result, receipt map[string]any) error {
	// Pre-Byzantium receipts have no status field; treat only an explicit
	// failure as a revert.
	if status, ok := receipt["status"].(string); ok && status != "0x1" {
		return fmt.Errorf("claim transaction %s reverted on-chain (status %s)", result.ClaimHash, status)
	}

	blockHex, ok := receipt["blockNumber"].(string)
	if !ok {
		return fmt.Errorf("claim transaction %s receipt has no block number", result.ClaimHash)
	}

	block, err := parseHexUint64(blockHex)
	if err != nil {
		return fmt.Errorf("claim transaction %s has an unparseable block number %q: %w",
			result.ClaimHash, blockHex, err)
	}

	result.Confirmed = true
	result.BlockNumber = block

	return nil
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
