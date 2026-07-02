package faucet

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestClaimLive runs a real claim against a live faucet. It is guarded by env
// vars so it never runs in CI by default:
//
//	FAUCET_LIVE_URL=https://faucet-agents.<network>.ethpandaops.io \
//	FAUCET_LIVE_ADDR=0x... \
//	go test -run TestClaimLive -v -timeout 6m ./pkg/faucet
func TestClaimLive(t *testing.T) {
	url := os.Getenv("FAUCET_LIVE_URL")
	addr := os.Getenv("FAUCET_LIVE_ADDR")
	if url == "" || addr == "" {
		t.Skip("set FAUCET_LIVE_URL and FAUCET_LIVE_ADDR to run the live e2e")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	res, err := New(url, nil).Claim(ctx, addr)
	if err != nil {
		t.Fatalf("live claim: %v", err)
	}

	if res.ClaimHash == "" {
		t.Fatal("claim returned no transaction hash")
	}

	t.Logf("claimed to %s: tx %s (%s wei)", res.Target, res.ClaimHash, res.AmountWei)
}
