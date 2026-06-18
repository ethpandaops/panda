package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// probeTimeout bounds the v2 capability probe so a slow or unreachable proxy
// falls back to v1 quickly rather than stalling startup.
const probeTimeout = 15 * time.Second

// ProbeV2 reports whether the proxy exposes the versioned /v2/embedding route
// and, if so, the embedding model it currently advertises. It POSTs an empty
// check request to /v2/embedding/check; a 200 response means v2 is available and
// its model is returned. Any non-200 status or transport error yields
// ("", false), signalling the caller to fall back to the legacy /embed routes.
func ProbeV2(ctx context.Context, proxyURL string, tokenFn func() string) (string, bool) {
	body, err := json.Marshal(embedCheckRequest{Hashes: []string{}})
	if err != nil {
		return "", false
	}

	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, proxyURL+"/v2/embedding/check", bytes.NewReader(body),
	)
	if err != nil {
		return "", false
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
		return "", false
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", false
	}

	var checkResp embedCheckResponse
	if err := json.NewDecoder(resp.Body).Decode(&checkResp); err != nil {
		return "", false
	}

	return checkResp.Model, true
}
