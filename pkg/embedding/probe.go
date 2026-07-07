package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// Protocol identifies the proxy embedding protocol negotiated by the server.
type Protocol string

const (
	ProtocolUnknown Protocol = ""
	ProtocolV1      Protocol = "v1"
	ProtocolV2      Protocol = "v2"
	ProtocolV3      Protocol = "v3"

	defaultV2Dimensions = 1536
)

// probeTimeout bounds each embedding capability probe so a slow or unreachable
// proxy reports unavailable quickly rather than stalling startup.
const probeTimeout = 15 * time.Second

// Probe negotiates the newest embedding protocol the proxy supports. v3 and v2
// are discovered by their check endpoints. v1 is the legacy datasources
// advertisement and has no dimensionality echo.
func Probe(ctx context.Context, proxyURL string, tokenFn func() string, fallbackModel string) (string, int, Protocol) {
	if model, dims, ok := ProbeV3(ctx, proxyURL, tokenFn); ok {
		return model, dims, ProtocolV3
	}

	if model, dims, ok := ProbeV2(ctx, proxyURL, tokenFn); ok {
		return model, dims, ProtocolV2
	}

	if fallbackModel != "" && ProbeV1(ctx, proxyURL, tokenFn) {
		return fallbackModel, 0, ProtocolV1
	}

	return "", 0, ProtocolUnknown
}

// ProbeV3 reports whether the proxy exposes /v3/embedding/check.
func ProbeV3(ctx context.Context, proxyURL string, tokenFn func() string) (string, int, bool) {
	model, dims, ok := probeCheck(ctx, proxyURL, embedCheckPathV3, tokenFn, embedCheckRequest{
		Hashes: []string{},
		Task:   taskQuery,
	})
	if !ok || dims <= 0 {
		return "", 0, false
	}

	return model, dims, true
}

// ProbeV2 reports whether the proxy exposes /v2/embedding/check.
func ProbeV2(ctx context.Context, proxyURL string, tokenFn func() string) (string, int, bool) {
	model, dims, ok := probeCheck(ctx, proxyURL, embedCheckPathV2, tokenFn, embedCheckRequest{Hashes: []string{}})
	if !ok {
		return "", 0, false
	}

	if dims < 0 {
		return "", 0, false
	}
	if dims == 0 {
		dims = defaultV2Dimensions
	}

	return model, dims, true
}

// ProbeV1 confirms the legacy /embed/check endpoint exists. The v1 embedding
// space comes from datasource discovery, so the response body is not decoded.
func ProbeV1(ctx context.Context, proxyURL string, tokenFn func() string) bool {
	body, err := json.Marshal(embedCheckRequest{Hashes: []string{}})
	if err != nil {
		return false
	}

	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, proxyURL+embedCheckPathV1, bytes.NewReader(body),
	)
	if err != nil {
		return false
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
		return false
	}
	defer func() { _ = resp.Body.Close() }()

	return resp.StatusCode == http.StatusOK
}

func probeCheck(
	ctx context.Context,
	proxyURL string,
	path string,
	tokenFn func() string,
	payload embedCheckRequest,
) (string, int, bool) {
	body, err := json.Marshal(payload)
	if err != nil {
		return "", 0, false
	}

	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, proxyURL+path, bytes.NewReader(body),
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

	if checkResp.Model == "" {
		return "", 0, false
	}

	return checkResp.Model, checkResp.Dimensions, true
}
