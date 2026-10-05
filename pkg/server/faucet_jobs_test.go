package server

import (
	"context"
	"errors"
	"math/big"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/attribution"
	"github.com/ethpandaops/panda/pkg/faucet"
)

func TestFaucetJobsDisconnectDuplicatesAndDelayedReceipt(t *testing.T) {
	var runs atomic.Int32
	submitted := make(chan struct{})
	receipt := make(chan struct{})
	j := newFaucetJobs(func(ctx context.Context, _, _ string, _ *big.Int, update func(faucet.Progress)) error {
		runs.Add(1)
		require.Equal(t, "original-caller", attribution.FromContext(ctx))
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		require.WithinDuration(t, time.Now().Add(faucetClaimTimeout), deadline, time.Second)
		update(faucet.Progress{Session: "session", Submitted: true, Result: &faucet.Result{ClaimHash: "0xtx", AmountWei: "1"}})
		close(submitted)
		select {
		case <-receipt:
			update(faucet.Progress{Session: "session", Submitted: true, Result: &faucet.Result{ClaimHash: "0xtx", AmountWei: "1", Confirmed: true, BlockNumber: 42}})
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	t.Cleanup(j.stop)
	ctx, disconnect := context.WithCancel(attribution.WithValue(context.Background(), "original-caller"))
	job, _, err := j.start(ctx, "network", "0xAB", nil, "")
	require.NoError(t, err)
	disconnect()
	<-submitted
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			recovered, _, err := j.start(context.Background(), "network", "0xab", nil, "alias")
			require.NoError(t, err)
			require.Equal(t, job.ID, recovered.ID)
		}()
	}
	wg.Wait()
	pending, ok := j.get(job.ID)
	require.True(t, ok)
	require.Equal(t, "submitted", pending.State)
	require.False(t, pending.Terminal)
	require.Equal(t, "0xtx", pending.ClaimHash)
	close(receipt)
	require.Eventually(t, func() bool { v, _ := j.get(job.ID); return v.Terminal }, time.Second, time.Millisecond)
	confirmed, _ := j.get(job.ID)
	require.Equal(t, "confirmed", confirmed.State)
	require.Equal(t, uint64(42), confirmed.BlockNumber)
	for _, key := range []string{"", "alias"} {
		recovered, _, err := j.start(context.Background(), "network", "0xab", nil, key)
		require.NoError(t, err)
		require.Equal(t, job.ID, recovered.ID)
	}
	require.Equal(t, int32(1), runs.Load())
	_, status, err := j.start(context.Background(), "network", "0xab", big.NewInt(2), "alias")
	require.Error(t, err)
	require.Equal(t, http.StatusConflict, status)
}

func TestFaucetJobsShutdownCapacityAndExpiry(t *testing.T) {
	j := newFaucetJobs(func(ctx context.Context, _, _ string, _ *big.Int, _ func(faucet.Progress)) error {
		<-ctx.Done()
		return ctx.Err()
	})
	t.Cleanup(j.stop)
	var first FaucetJob
	for n := range faucetMaxActiveJobs {
		job, _, err := j.start(context.Background(), "network", big.NewInt(int64(n)).String(), nil, "")
		require.NoError(t, err)
		if n == 0 {
			first = job
		}
	}
	_, status, err := j.start(context.Background(), "network", "extra", nil, "")
	require.Error(t, err)
	require.Equal(t, http.StatusTooManyRequests, status)
	j.stop()
	require.Eventually(t, func() bool { job, _ := j.get(first.ID); return job.Terminal }, time.Second, time.Millisecond)
	job, _ := j.get(first.ID)
	require.Equal(t, "failed", job.State)
	require.Contains(t, job.Error, "canceled")
	_, status, err = j.start(context.Background(), "network", "new", nil, "")
	require.Error(t, err)
	require.Equal(t, http.StatusServiceUnavailable, status)
	j.mu.Lock()
	j.jobs[first.ID].finishedAt = time.Now().Add(-faucetJobRetention)
	j.mu.Unlock()
	_, ok := j.get(first.ID)
	require.False(t, ok)
}

func TestFaucetJobsUncertainSubmissionRecovery(t *testing.T) {
	j := newFaucetJobs(func(_ context.Context, _, _ string, _ *big.Int, update func(faucet.Progress)) error {
		update(faucet.Progress{Session: "session", Submitted: true})
		return errors.New("lost submission response")
	})
	t.Cleanup(j.stop)
	job, _, err := j.start(context.Background(), "network", "address", big.NewInt(99), "recover")
	require.NoError(t, err)
	require.Eventually(t, func() bool { v, _ := j.get(job.ID); return v.Terminal }, time.Second, time.Millisecond)
	uncertain, _ := j.get(job.ID)
	require.Equal(t, "submitted", uncertain.State)
	require.Equal(t, "session", uncertain.Session)
	recovered, _, err := j.start(context.Background(), "network", "address", big.NewInt(99), "recover")
	require.NoError(t, err)
	require.Equal(t, job.ID, recovered.ID)
	confirmed := j.recover(job.ID, &faucet.Result{ClaimHash: "0xlate", AmountWei: "99", Confirmed: true, BlockNumber: 7}, nil)
	require.Equal(t, "confirmed", confirmed.State)
	require.Empty(t, confirmed.Error)
	require.Equal(t, "0xlate", confirmed.ClaimHash)
}

func TestFaucetJobsExplicitRejection(t *testing.T) {
	j := newFaucetJobs(func(_ context.Context, _, _ string, _ *big.Int, update func(faucet.Progress)) error {
		update(faucet.Progress{Session: "session", Submitted: true})
		update(faucet.Progress{Session: "session", Rejected: true})
		return errors.New("daily limit")
	})
	t.Cleanup(j.stop)
	job, _, err := j.start(context.Background(), "network", "address", nil, "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { v, _ := j.get(job.ID); return v.Terminal }, time.Second, time.Millisecond)
	failed, _ := j.get(job.ID)
	require.Equal(t, "failed", failed.State)
	require.Equal(t, "daily limit", failed.Error)
}
