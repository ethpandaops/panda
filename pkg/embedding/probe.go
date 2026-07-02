package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// probeTimeout bounds the embedding capability probe so a slow or unreachable
// proxy reports unavailable quickly rather than stalling startup.
const probeTimeout = 15 * time.Second

// Probe reports whether the proxy serves embedding and, if so, the embedding
// model and output dimensionality it currently advertises. It POSTs an empty
// check request (task is required on the wire, so the probe carries one) to
// /embedding/check; a 200 response means embedding is available. Any non-200
// status or transport error yields ("", 0, false) — embedding is unavailable.
func Probe(ctx context.Context, proxyURL string, tokenFn func() string) (string, int, bool) {
	body, err := json.Marshal(embedCheckRequest{Hashes: []string{}, Task: taskQuery})
	if err != nil {
		return "", 0, false
	}

	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, proxyURL+embedCheckPath, bytes.NewReader(body),
	)
	if err != nil {
		return "", 0, false
	}

	req.Header.Set("Content-Type", "application/json")

	if tokenFn != nil {
		if token := tokenFn(); token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}

	client := &http.Client{Timeout: probeTimeout}

	resp, err := client.Do(req)
	if err != nil {
		return "", 0, false
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", 0, false
	}

	var checkResp embedCheckResponse
	if err := json.NewDecoder(resp.Body).Decode(&checkResp); err != nil {
		return "", 0, false
	}

	return checkResp.Model, checkResp.Dimensions, true
}
