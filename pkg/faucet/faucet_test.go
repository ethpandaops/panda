package faucet

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"golang.org/x/crypto/argon2"
)

// mockFaucet is a faithful in-memory PoWFaucet agent-REST server: it advertises
// Argon2id/v19 params and validates each submitted share by recomputing the
// hash exactly as the real faucet does, so a share that does not meet the
// difficulty target is rejected. This proves the client mines correctly.
type mockFaucet struct {
	difficulty  int
	timeCost    uint32
	memoryCost  uint32
	keyLength   uint32
	preimage    []byte // raw preimage bytes
	shareReward int64
	minClaim    int64
	claimHash   string

	balance     int64
	nonceCursor uint64
	claimed     bool
}

func (m *mockFaucet) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/startSession", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResp(w, map[string]any{"session": "sess-1"})
	})

	mux.HandleFunc("/api/getFaucetConfig", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResp(w, map[string]any{"minClaim": m.minClaim})
	})

	mux.HandleFunc("/api/powChallenge", func(w http.ResponseWriter, _ *http.Request) {
		start := m.nonceCursor
		m.nonceCursor += 100000

		writeJSONResp(w, map[string]any{
			"algo": "argon2",
			"params": map[string]any{
				"type": 2, "version": 19, "timeCost": m.timeCost,
				"memoryCost": m.memoryCost, "parallelization": 1, "keyLength": m.keyLength,
			},
			"difficulty": m.difficulty,
			"preimage":   base64.StdEncoding.EncodeToString(m.preimage),
			"nonceStart": start,
			"nonceCount": uint64(100000),
		})
	})

	mux.HandleFunc("/api/powSubmit", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Nonce uint64 `json:"nonce"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)

		buf := make([]byte, 8)
		binary.BigEndian.PutUint64(buf, body.Nonce)
		hash := argon2.IDKey(buf, m.preimage, m.timeCost, m.memoryCost, 1, m.keyLength)
		mask := difficultyMask(m.difficulty)

		if hex.EncodeToString(hash)[:len(mask)] > mask {
			writeJSONResp(w, map[string]any{"valid": false, "error": "does not meet difficulty target"})

			return
		}

		m.balance += m.shareReward
		writeJSONResp(w, map[string]any{"valid": true, "balance": strconv.FormatInt(m.balance, 10)})
	})

	mux.HandleFunc("/api/powCloseSession", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResp(w, map[string]any{"status": "claimable"})
	})

	mux.HandleFunc("/api/claimReward", func(w http.ResponseWriter, _ *http.Request) {
		m.claimed = true
		writeJSONResp(w, map[string]any{"claimStatus": "queue"})
	})

	mux.HandleFunc("/api/getSessionStatus", func(w http.ResponseWriter, _ *http.Request) {
		if !m.claimed {
			writeJSONResp(w, map[string]any{"status": "claiming", "claimStatus": "pending"})

			return
		}

		writeJSONResp(w, map[string]any{
			"status": "finished", "claimStatus": "confirmed",
			"claimHash": m.claimHash, "balance": strconv.FormatInt(m.balance, 10),
		})
	})

	return mux
}

func writeJSONResp(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func TestClaim(t *testing.T) {
	// Low difficulty + tiny argon2 params keep the test fast while still
	// exercising real mining, submission, and difficulty validation.
	mock := &mockFaucet{
		difficulty:  6,
		timeCost:    1,
		memoryCost:  512,
		keyLength:   16,
		preimage:    []byte("12345678"),
		shareReward: 500_000_000_000_000_000,   // 0.5 ETH
		minClaim:    1_000_000_000_000_000_000, // 1 ETH -> needs 2 shares
		claimHash:   "0xabc123",
	}

	srv := httptest.NewServer(mock.handler())
	defer srv.Close()

	res, err := New(srv.URL, srv.Client()).Claim(context.Background(), "0xTarget")
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}

	if res.ClaimHash != mock.claimHash {
		t.Fatalf("claim hash = %q, want %q", res.ClaimHash, mock.claimHash)
	}

	if !mock.claimed {
		t.Fatal("faucet was never asked to claim")
	}

	if mock.balance < mock.minClaim {
		t.Fatalf("mined balance %d below minClaim %d", mock.balance, mock.minClaim)
	}
}

func TestSupportedRejectsNonStandardParams(t *testing.T) {
	var ch powChallenge
	ch.Algo = "argon2"
	ch.Params.Type = 0 // Argon2d — not what x/crypto computes
	ch.Params.Version = 19

	if err := supported(ch); err == nil {
		t.Fatal("expected Argon2d (type 0) to be rejected")
	}

	ch.Params.Type = 2
	ch.Params.Version = 13 // non-standard version

	if err := supported(ch); err == nil {
		t.Fatal("expected version 13 to be rejected")
	}

	ch.Params.Version = 19
	if err := supported(ch); err != nil {
		t.Fatalf("Argon2id/v19 should be supported: %v", err)
	}
}

func TestDifficultyMask(t *testing.T) {
	for _, tc := range []struct {
		difficulty int
		want       string
	}{
		{10, "0040"},
		{14, "0004"},
		{8, "0100"},
	} {
		if got := difficultyMask(tc.difficulty); got != tc.want {
			t.Errorf("difficultyMask(%d) = %q, want %q", tc.difficulty, got, tc.want)
		}
	}
}
