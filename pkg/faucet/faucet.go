// Package faucet mines a PoWFaucet agent-REST challenge and claims the reward
// for a target address, with no browser, WebSocket, or captcha.
//
// It requires the faucet's PoW to be Argon2id (type 2) at version 19 — the
// standard variant implemented by golang.org/x/crypto/argon2 — so mining runs
// natively with no cgo and no external dependency.
package faucet

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

// userAgent is set on every request: the faucet ingress rejects some default
// client user-agents with 403, so we always send an explicit one.
const userAgent = "panda-faucet/1"

// pollInterval and pollAttempts bound the wait for on-chain claim confirmation.
const (
	pollInterval = 3 * time.Second
	pollAttempts = 40
)

// Result is the outcome of a successful claim.
type Result struct {
	Session   string `json:"session"`
	Target    string `json:"target"`
	ClaimHash string `json:"claim_hash"`
	AmountWei string `json:"amount_wei"`
}

// Transport issues one faucet HTTP request and returns the response body and
// status code. It lets the mining flow run either against a faucet URL directly
// (tests, CLI) or through the panda proxy — the only authenticated network path
// in production, so the faucet needs no public surface.
type Transport interface {
	Do(ctx context.Context, method, path string, body []byte) (respBody []byte, status int, err error)
}

// Client drives the PoWFaucet agent-REST flow over a Transport.
type Client struct {
	t Transport
}

// NewWithTransport returns a Client that issues requests through t (e.g. the
// proxy-routed transport used by the evm.faucet server operation).
func NewWithTransport(t Transport) *Client { return &Client{t: t} }

// New returns a Client that talks directly to a faucet base URL, e.g.
// https://faucet-agents.<network>.ethpandaops.io. Used by tests and direct use.
func New(baseURL string, httpClient *http.Client) *Client {
	return NewWithTransport(newHTTPTransport(baseURL, httpClient))
}

// Claim runs the full agent flow for address: start a session, mine PoW shares
// until the balance covers the minimum drop, close the session, submit the
// claim, and poll until the claim transaction confirms. It returns the claim
// transaction hash.
func (c *Client) Claim(ctx context.Context, address string) (*Result, error) {
	session, err := c.startSession(ctx, address)
	if err != nil {
		return nil, err
	}

	minClaim, err := c.minClaim(ctx, session)
	if err != nil {
		return nil, err
	}

	if err := c.mine(ctx, session, minClaim); err != nil {
		return nil, err
	}

	if err := c.post(ctx, "/api/powCloseSession?session="+session, nil, nil); err != nil {
		return nil, fmt.Errorf("closing session: %w", err)
	}

	if err := c.post(ctx, "/api/claimReward", map[string]string{"session": session}, nil); err != nil {
		return nil, fmt.Errorf("submitting claim: %w", err)
	}

	return c.awaitClaim(ctx, session, address)
}

// startSession opens a PoW session for the target address and returns its id.
func (c *Client) startSession(ctx context.Context, address string) (string, error) {
	var resp struct {
		Session      string `json:"session"`
		FailedCode   string `json:"failedCode"`
		FailedReason string `json:"failedReason"`
	}

	if err := c.post(ctx, "/api/startSession", map[string]string{"addr": address}, &resp); err != nil {
		return "", fmt.Errorf("starting session: %w", err)
	}

	if resp.Session == "" {
		reason := resp.FailedReason
		if reason == "" {
			reason = resp.FailedCode
		}

		return "", fmt.Errorf("faucet refused session: %s", reason)
	}

	return resp.Session, nil
}

// minClaim reads the minimum claimable amount (wei) for the session.
func (c *Client) minClaim(ctx context.Context, session string) (*big.Int, error) {
	var resp struct {
		MinClaim json.Number `json:"minClaim"`
	}

	if err := c.get(ctx, "/api/getFaucetConfig?session="+session, &resp); err != nil {
		return nil, fmt.Errorf("reading faucet config: %w", err)
	}

	minClaim, ok := new(big.Int).SetString(resp.MinClaim.String(), 10)
	if !ok || minClaim.Sign() <= 0 {
		// Fall back to 1 ETH if the faucet does not advertise a minimum.
		minClaim, _ = new(big.Int).SetString("1000000000000000000", 10)
	}

	return minClaim, nil
}

// powChallenge is a single mining assignment from the faucet.
type powChallenge struct {
	Algo   string `json:"algo"`
	Params struct {
		Type            int    `json:"type"`
		Version         int    `json:"version"`
		TimeCost        uint32 `json:"timeCost"`
		MemoryCost      uint32 `json:"memoryCost"`
		Parallelization uint8  `json:"parallelization"`
		KeyLength       uint32 `json:"keyLength"`
	} `json:"params"`
	Difficulty int    `json:"difficulty"`
	Preimage   string `json:"preimage"`
	NonceStart uint64 `json:"nonceStart"`
	NonceCount uint64 `json:"nonceCount"`
}

// mine fetches challenges and submits valid shares until the session balance
// reaches minClaim.
func (c *Client) mine(ctx context.Context, session string, minClaim *big.Int) error {
	balance := new(big.Int)

	for balance.Cmp(minClaim) < 0 {
		if err := ctx.Err(); err != nil {
			return err
		}

		var ch powChallenge
		if err := c.get(ctx, "/api/powChallenge?session="+session, &ch); err != nil {
			return fmt.Errorf("fetching challenge: %w", err)
		}

		if err := supported(ch); err != nil {
			return err
		}

		salt, err := base64.StdEncoding.DecodeString(ch.Preimage)
		if err != nil {
			return fmt.Errorf("decoding preimage: %w", err)
		}

		nonce, found, err := solve(ctx, ch, salt)
		if err != nil {
			return err
		}

		if !found {
			continue // no share in this range; the next challenge advances it
		}

		var sub struct {
			Valid   bool   `json:"valid"`
			Balance string `json:"balance"`
			Error   string `json:"error"`
		}

		body := map[string]any{"session": session, "nonce": nonce}
		if err := c.post(ctx, "/api/powSubmit", body, &sub); err != nil {
			return fmt.Errorf("submitting share: %w", err)
		}

		if !sub.Valid {
			return fmt.Errorf("faucet rejected share: %s", sub.Error)
		}

		if _, ok := balance.SetString(sub.Balance, 10); !ok {
			return fmt.Errorf("unparseable balance %q", sub.Balance)
		}
	}

	return nil
}

// supported verifies the challenge is Argon2id at version 19, which is what
// x/crypto/argon2.IDKey computes.
func supported(ch powChallenge) error {
	if ch.Algo != "argon2" || ch.Params.Type != 2 || ch.Params.Version != 19 {
		return fmt.Errorf(
			"unsupported PoW: algo=%q type=%d version=%d (need argon2/type=2/version=19)",
			ch.Algo, ch.Params.Type, ch.Params.Version,
		)
	}

	return nil
}

// solve scans the challenge's nonce range for a hash that meets the difficulty
// target. ctx cancellation is checked periodically so long scans abort.
func solve(ctx context.Context, ch powChallenge, salt []byte) (uint64, bool, error) {
	mask := difficultyMask(ch.Difficulty)
	p := ch.Params
	nonceBuf := make([]byte, 8)

	for i := uint64(0); i < ch.NonceCount; i++ {
		if i%256 == 0 {
			if err := ctx.Err(); err != nil {
				return 0, false, err
			}
		}

		nonce := ch.NonceStart + i
		binary.BigEndian.PutUint64(nonceBuf, nonce)

		hash := argon2.IDKey(nonceBuf, salt, p.TimeCost, p.MemoryCost, p.Parallelization, p.KeyLength)
		if hex.EncodeToString(hash)[:len(mask)] <= mask {
			return nonce, true, nil
		}
	}

	return 0, false, nil
}

// difficultyMask returns the hex prefix a hash must be <= to satisfy the
// difficulty. It mirrors PoWValidatorWorker.getDifficultyMask exactly.
func difficultyMask(difficulty int) string {
	byteCount := difficulty/8 + 1
	bitCount := difficulty - (byteCount-1)*8
	maxValue := 1 << (8 - bitCount)

	return fmt.Sprintf("%0*x", byteCount*2, maxValue)
}

// awaitClaim polls session status until the claim confirms or fails.
func (c *Client) awaitClaim(ctx context.Context, session, address string) (*Result, error) {
	for attempt := 0; attempt < pollAttempts; attempt++ {
		var st struct {
			Status      string `json:"status"`
			ClaimStatus string `json:"claimStatus"`
			ClaimHash   string `json:"claimHash"`
			Balance     string `json:"balance"`
		}

		if err := c.get(ctx, "/api/getSessionStatus?session="+session, &st); err != nil {
			return nil, fmt.Errorf("polling claim: %w", err)
		}

		switch st.ClaimStatus {
		case "confirmed":
			return &Result{Session: session, Target: address, ClaimHash: st.ClaimHash, AmountWei: st.Balance}, nil
		case "failed":
			return nil, fmt.Errorf("claim failed (tx %s)", st.ClaimHash)
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pollInterval):
		}
	}

	return nil, fmt.Errorf("claim not confirmed after %d polls", pollAttempts)
}

// get performs a GET and decodes the JSON body into out (may be nil).
func (c *Client) get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}

// post performs a POST with a JSON body and decodes the response into out.
func (c *Client) post(ctx context.Context, path string, body, out any) error {
	return c.do(ctx, http.MethodPost, path, body, out)
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var payload []byte
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}

		payload = encoded
	}

	data, status, err := c.t.Do(ctx, method, path, payload)
	if err != nil {
		return err
	}

	if status < 200 || status >= 300 {
		return fmt.Errorf("faucet returned %d: %s", status, strings.TrimSpace(string(data)))
	}

	if out == nil {
		return nil
	}

	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}

	return nil
}

// httpTransport talks to a faucet base URL directly.
type httpTransport struct {
	baseURL string
	http    *http.Client
}

func newHTTPTransport(baseURL string, httpClient *http.Client) *httpTransport {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}

	return &httpTransport{baseURL: strings.TrimRight(baseURL, "/"), http: httpClient}
}

func (t *httpTransport) Do(ctx context.Context, method, path string, body []byte) ([]byte, int, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, t.baseURL+path, reader)
	if err != nil {
		return nil, 0, err
	}

	req.Header.Set("User-Agent", userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := t.http.Do(req)
	if err != nil {
		return nil, http.StatusBadGateway, err
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}

	return data, resp.StatusCode, nil
}
