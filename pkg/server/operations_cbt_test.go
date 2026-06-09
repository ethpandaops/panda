package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ethpandaops/cartographoor/pkg/discovery"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/operations"
)

func TestCBTIDPassthroughEscapesIdentifier(t *testing.T) {
	t.Parallel()

	transport := &recordingTransport{body: `{"ok":true}`, contentType: "application/json"}
	svc := newCBTOperationService(transport)
	rec := httptest.NewRecorder()

	handled := svc.handleCBTOperation("cbt.get_external_model", rec, newCBTOpRequest(t, map[string]any{
		"network": "testnet",
		"id":      "abc/def?x=1",
	}))

	require.True(t, handled)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, transport.last)
	assert.Equal(t, "/api/v1/models/external/abc%2Fdef%3Fx=1", transport.last.URL.EscapedPath())
	assert.Empty(t, transport.last.URL.RawQuery)
}

func TestCBTLinkModelEscapesPathSegments(t *testing.T) {
	t.Parallel()

	svc := newCBTOperationService(nil)
	rec := httptest.NewRecorder()

	handled := svc.handleCBTOperation("cbt.link_model", rec, newCBTOpRequest(t, map[string]any{
		"network": "testnet",
		"id":      "db/name.table/name?x=1",
	}))

	require.True(t, handled)
	require.Equal(t, http.StatusOK, rec.Code)

	var response operations.Response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	data, ok := response.Data.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "https://cbt.testnet.ethpandaops.io/models/db%2Fname/table%2Fname%3Fx=1", data["url"])
}

func newCBTOperationService(transport http.RoundTripper) *service {
	log := logrus.New()
	log.SetOutput(io.Discard)

	httpClient := http.DefaultClient
	if transport != nil {
		httpClient = &http.Client{Transport: transport}
	}

	return &service{
		log:        log,
		httpClient: httpClient,
		cartographoorClient: cbtOperationCartographoor{
			networks: map[string]discovery.Network{
				"testnet": {
					Name:   "testnet",
					Status: "active",
				},
			},
		},
	}
}

func newCBTOpRequest(t *testing.T, args map[string]any) *http.Request {
	t.Helper()

	body, err := json.Marshal(operations.Request{Args: args})
	require.NoError(t, err)

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/runtime/operations/cbt",
		io.NopCloser(bytes.NewReader(body)),
	)
	req.Header.Set("Content-Type", "application/json")

	return req
}

type cbtOperationCartographoor struct {
	networks map[string]discovery.Network
}

func (c cbtOperationCartographoor) Start(_ context.Context) error { return nil }
func (c cbtOperationCartographoor) Stop() error                   { return nil }
func (c cbtOperationCartographoor) GetAllNetworks() map[string]discovery.Network {
	return c.networks
}
func (c cbtOperationCartographoor) GetActiveNetworks() map[string]discovery.Network {
	return c.networks
}
func (c cbtOperationCartographoor) GetNetwork(name string) (discovery.Network, bool) {
	network, ok := c.networks[name]
	return network, ok
}
func (c cbtOperationCartographoor) GetGroup(_ string) (map[string]discovery.Network, bool) {
	return nil, false
}
func (c cbtOperationCartographoor) GetGroups() []string { return nil }
func (c cbtOperationCartographoor) IsDevnet(_ discovery.Network) bool {
	return false
}
func (c cbtOperationCartographoor) GetClusters(_ discovery.Network) []string {
	return nil
}
