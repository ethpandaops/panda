package server

import (
	"context"
	"errors"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/ethpandaops/panda/pkg/faucet"
	"github.com/ethpandaops/panda/pkg/operations"
)

const (
	faucetJobRetention    = time.Hour
	faucetMaxActiveJobs   = 8
	faucetMaxRetainedJobs = 256
)

// FaucetJob is a snapshot. Submitted means submission was attempted, not that
// a transaction has a receipt. Terminal stops background work, not recovery.
type FaucetJob struct {
	ID          string    `json:"job_id"`
	Network     string    `json:"network"`
	Address     string    `json:"address"`
	AmountWei   string    `json:"amount_wei,omitempty"`
	State       string    `json:"state"`
	Session     string    `json:"session,omitempty"`
	ClaimHash   string    `json:"claim_hash,omitempty"`
	BlockNumber uint64    `json:"block_number,omitempty"`
	Error       string    `json:"error,omitempty"`
	Terminal    bool      `json:"terminal"`
	StartedAt   time.Time `json:"started_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type faucetJobEntry struct {
	FaucetJob
	requestKeys     []string
	requestedAmount string
	addressKey      string
	finishedAt      time.Time
	cancel          context.CancelFunc
}

type faucetJobRunner func(context.Context, string, string, *big.Int, func(faucet.Progress)) error

// The local server owns jobs. Records survive execution/request cancellation,
// but not a server restart; callers must keep the original wallet and job ID.
type faucetJobs struct {
	mu       sync.Mutex
	jobs     map[string]*faucetJobEntry
	requests map[string]string
	active   map[string]string
	closed   bool
	run      faucetJobRunner
}

func newFaucetJobs(run faucetJobRunner) *faucetJobs {
	return &faucetJobs{jobs: make(map[string]*faucetJobEntry), requests: make(map[string]string), active: make(map[string]string), run: run}
}

func (s *service) faucetJobStore() *faucetJobs {
	s.faucetJobsOnce.Do(func() { s.faucetJobsInst = newFaucetJobs(s.runFaucetJob) })
	return s.faucetJobsInst
}

func (s *service) handleEVMFaucetStart(w http.ResponseWriter, r *http.Request) {
	network, address, amount, args, ok := s.decodeFaucetRequest(w, r)
	if !ok {
		return
	}
	requestID := ""
	if raw, exists := args["request_id"]; exists {
		var valid bool
		requestID, valid = raw.(string)
		if !valid || len(requestID) == 0 || len(requestID) > 128 {
			writeAPIError(w, http.StatusBadRequest, "request_id must be a nonempty string of at most 128 bytes")
			return
		}
	}
	job, status, err := s.faucetJobStore().start(r.Context(), network, address, amount, requestID)
	if err != nil {
		writeAPIError(w, status, err.Error())
		return
	}
	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{Kind: operations.ResultKindObject, Data: job})
}

func (s *service) handleEVMFaucetStatus(w http.ResponseWriter, r *http.Request) {
	if s.credentials == nil || !s.credentials.Status().Authenticated {
		writeAPIError(w, http.StatusUnauthorized, "panda auth required: run 'panda auth login' first")
		return
	}
	req, err := decodeOperationRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	id, err := requiredStringArg(req.Args, "job_id")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	job, ok := s.faucetJobStore().get(id)
	if !ok {
		writeAPIError(w, http.StatusNotFound, "faucet job unavailable or expired; check the original address balance and receipt before retrying (jobs do not survive server restart)")
		return
	}
	// After background expiry, a short status request can recover a delayed hash
	// or receipt without starting another mining session.
	if job.Terminal && job.State == "submitted" {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		result := &faucet.Result{Session: job.Session, Target: job.Address, ClaimHash: job.ClaimHash, AmountWei: job.AmountWei}
		var recoveryErr error
		if result.ClaimHash == "" && result.Session != "" {
			result, recoveryErr = faucet.NewWithTransport(&proxyFaucetTransport{s: s, network: job.Network}).PollClaim(ctx, job.Session, job.Address)
		}
		if result != nil && result.ClaimHash != "" && recoveryErr == nil {
			raw, _, err := s.ethNodeExecutionRPC(ctx, job.Network, faucetReceiptInstance, "eth_getTransactionReceipt", []any{result.ClaimHash})
			if err == nil {
				if receipt, ok := raw.(map[string]any); ok {
					recoveryErr = applyFaucetReceipt(result, receipt)
				}
			}
			job = s.faucetJobStore().recover(job.ID, result, recoveryErr)
		} else if errors.Is(recoveryErr, faucet.ErrClaimRejected) {
			job = s.faucetJobStore().recover(job.ID, nil, recoveryErr)
		}
	}
	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{Kind: operations.ResultKindObject, Data: job})
}

func (j *faucetJobs) prune(now time.Time) {
	for id, entry := range j.jobs {
		if entry.Terminal && now.Sub(entry.finishedAt) >= faucetJobRetention {
			for _, key := range entry.requestKeys {
				delete(j.requests, key)
			}
			delete(j.jobs, id)
		}
	}
}

func (j *faucetJobs) start(parent context.Context, network, address string, amount *big.Int, requestID string) (FaucetJob, int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return FaucetJob{}, http.StatusServiceUnavailable, errors.New("server is stopping")
	}
	j.prune(time.Now())
	amountString := ""
	if amount != nil {
		amountString = amount.String()
		amount = new(big.Int).Set(amount)
	}
	addressKey := network + ":" + strings.ToLower(address)
	// Separate explicit request IDs from default recovery keys.
	requestKey := addressKey + ":default:" + amountString
	if requestID != "" {
		requestKey = addressKey + ":request:" + requestID
	}
	if id := j.requests[requestKey]; id != "" {
		entry := j.jobs[id]
		if entry.requestedAmount != amountString {
			return FaucetJob{}, http.StatusConflict, errors.New("request_id already used with a different amount")
		}
		return entry.FaucetJob, http.StatusOK, nil
	}
	if id := j.active[addressKey]; id != "" {
		entry := j.jobs[id]
		if entry.requestedAmount != amountString {
			return FaucetJob{}, http.StatusConflict, errors.New("address already has an active claim for a different amount")
		}
		// Register the alias so a lost start reply remains recoverable after completion.
		if len(j.requests) >= 1024 {
			return FaucetJob{}, http.StatusTooManyRequests, errors.New("local faucet recovery key capacity reached")
		}
		j.requests[requestKey] = id
		entry.requestKeys = append(entry.requestKeys, requestKey)
		return entry.FaucetJob, http.StatusOK, nil
	}
	if len(j.active) >= faucetMaxActiveJobs || len(j.jobs) >= faucetMaxRetainedJobs || len(j.requests) >= 1024 {
		return FaucetJob{}, http.StatusTooManyRequests, errors.New("local faucet job capacity reached; poll existing jobs or retry later")
	}
	started := time.Now().UTC()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), faucetClaimTimeout)
	entry := &faucetJobEntry{FaucetJob: FaucetJob{
		ID: uuid.NewString(), Network: network, Address: address, AmountWei: amountString,
		State: "mining", StartedAt: started, ExpiresAt: started.Add(faucetClaimTimeout),
	}, requestKeys: []string{requestKey}, requestedAmount: amountString, addressKey: addressKey, cancel: cancel}
	j.jobs[entry.ID] = entry
	j.requests[requestKey] = entry.ID
	j.active[addressKey] = entry.ID
	go func() {
		defer cancel()
		err := j.run(ctx, network, address, amount, func(progress faucet.Progress) {
			j.mu.Lock()
			defer j.mu.Unlock()
			entry.Session = progress.Session
			if progress.Submitted {
				entry.State = "submitted"
			}
			if progress.Rejected {
				entry.State = "failed"
			}
			if progress.Result != nil {
				entry.ClaimHash = progress.Result.ClaimHash
				entry.AmountWei = progress.Result.AmountWei
				entry.BlockNumber = progress.Result.BlockNumber
				if progress.Result.Confirmed {
					entry.State = "confirmed"
				}
			}
		})
		j.mu.Lock()
		defer j.mu.Unlock()
		entry.Terminal = true
		entry.finishedAt = time.Now()
		delete(j.active, addressKey)
		if err != nil {
			entry.Error = err.Error()
			if entry.State == "mining" {
				entry.State = "failed"
			}
		}
	}()
	return entry.FaucetJob, http.StatusOK, nil
}

func (j *faucetJobs) get(id string) (FaucetJob, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.prune(time.Now())
	entry, ok := j.jobs[id]
	if !ok {
		return FaucetJob{}, false
	}
	return entry.FaucetJob, true
}

func (j *faucetJobs) recover(id string, result *faucet.Result, err error) FaucetJob {
	j.mu.Lock()
	defer j.mu.Unlock()
	entry := j.jobs[id]
	if entry == nil {
		return FaucetJob{}
	}
	if result != nil {
		entry.ClaimHash = result.ClaimHash
		entry.AmountWei = result.AmountWei
		entry.BlockNumber = result.BlockNumber
		if result.Confirmed {
			entry.State = "confirmed"
			entry.Error = ""
		}
	}
	if err != nil {
		entry.State = "failed"
		entry.Error = err.Error()
	}
	return entry.FaucetJob
}

func (j *faucetJobs) stop() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.closed = true
	for _, entry := range j.jobs {
		if !entry.Terminal {
			entry.cancel()
		}
	}
}

func (s *service) runFaucetJob(ctx context.Context, network, address string, amount *big.Int, update func(faucet.Progress)) error {
	client := faucet.NewWithTransport(&proxyFaucetTransport{s: s, network: network})
	result, err := client.ClaimAmountWithProgress(ctx, address, amount, update)
	if err != nil {
		return err
	}
	for {
		if err := s.awaitFaucetReceipt(ctx, network, result); err != nil {
			update(faucet.Progress{Session: result.Session, Rejected: true, Result: result})
			return err
		}
		update(faucet.Progress{Session: result.Session, Submitted: true, Result: result})
		if result.Confirmed {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}
