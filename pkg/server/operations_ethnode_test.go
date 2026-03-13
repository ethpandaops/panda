package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateEthNodeNodeRequest(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		network     string
		instance    string
		wantErrText string
	}{
		{name: "valid", network: "mainnet", instance: "node-1"},
		{name: "missing network", instance: "node-1", wantErrText: "network is required"},
		{name: "missing instance", network: "mainnet", wantErrText: "instance is required"},
		{name: "invalid network", network: "Mainnet", instance: "node-1", wantErrText: "invalid network name"},
		{name: "invalid instance", network: "mainnet", instance: "node_1", wantErrText: "invalid instance name"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			request, err := validateEthNodeNodeRequest(tc.network, tc.instance)
			if tc.wantErrText == "" {
				require.NoError(t, err)
				assert.Equal(t, tc.network, request.Network)
				assert.Equal(t, tc.instance, request.Instance)
				return
			}

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErrText)
		})
	}
}

func TestParseEthNodePathArg(t *testing.T) {
	t.Parallel()

	path, err := parseEthNodePathArg(map[string]any{"path": "eth/v1/node/version"})
	require.NoError(t, err)
	assert.Equal(t, "/eth/v1/node/version", path)

	path, err = parseEthNodePathArg(map[string]any{"path": "/eth/v1/node/version"})
	require.NoError(t, err)
	assert.Equal(t, "/eth/v1/node/version", path)

	_, err = parseEthNodePathArg(map[string]any{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path is required")
}

func TestEthNodeHexPayload(t *testing.T) {
	t.Parallel()

	blockPayload, err := ethNodeHexPayload("block_number", "0x10", 16)
	require.NoError(t, err)
	assert.JSONEq(t, `{"block_number":16,"hex":"0x10"}`, mustJSONString(t, blockPayload))

	peerPayload, err := ethNodeHexPayload("peer_count", "0x05", 5)
	require.NoError(t, err)
	assert.JSONEq(t, `{"hex":"0x05","peer_count":5}`, mustJSONString(t, peerPayload))

	chainPayload, err := ethNodeHexPayload("chain_id", "0x01", 1)
	require.NoError(t, err)
	assert.JSONEq(t, `{"chain_id":1,"hex":"0x01"}`, mustJSONString(t, chainPayload))

	_, err = ethNodeHexPayload("unsupported", "0x0", 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported ethnode hex field")
}

func TestEthNodeBeaconRequestRawEncodesParamsAndDefaultsContentType(t *testing.T) {
	t.Parallel()

	client := &http.Client{
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "/beacon/mainnet/node-1/eth/v1/node/version", r.URL.Path)
			assert.Equal(t, url.Values{"limit": []string{"2"}, "state": []string{"head"}}.Encode(), r.URL.Query().Encode())
			assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			assert.JSONEq(t, `{"key":"value"}`, string(body))

			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"data":{"version":"Lighthouse"}}`)),
			}, nil
		}),
	}

	srv := newOperationTestService(&stubProxyService{url: "http://proxy.test"}, nil, client)

	body, contentType, status, err := srv.ethNodeBeaconRequestRaw(
		context.Background(),
		http.MethodPost,
		"mainnet",
		"node-1",
		"/eth/v1/node/version",
		map[string]any{"state": "head", "limit": 2},
		map[string]any{"key": "value"},
	)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, "application/json", contentType)
	assert.JSONEq(t, `{"data":{"version":"Lighthouse"}}`, string(body))
}

func TestEthNodeExecutionRPCRawReportsJSONRPCErrors(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/execution/mainnet/node-1/", r.URL.Path)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		assert.JSONEq(t, `{"id":1,"jsonrpc":"2.0","method":"eth_chainId","params":["latest"]}`, string(body))

		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"boom"}}`))
	}))
	defer upstream.Close()

	srv := newOperationTestService(&stubProxyService{url: upstream.URL}, nil, upstream.Client())

	_, _, status, err := srv.ethNodeExecutionRPCRaw(context.Background(), "mainnet", "node-1", "eth_chainId", []any{"latest"})
	require.Error(t, err)
	assert.Equal(t, http.StatusBadGateway, status)
	assert.Contains(t, err.Error(), "JSON-RPC error -32000: boom")
}

func mustJSONString(t *testing.T, value any) string {
	t.Helper()

	data, err := json.Marshal(value)
	require.NoError(t, err)

	return strings.TrimSpace(string(data))
}
