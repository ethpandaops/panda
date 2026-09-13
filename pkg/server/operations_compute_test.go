package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/proxy"
	"github.com/ethpandaops/panda/pkg/proxy/handlers"
	"github.com/ethpandaops/panda/pkg/types"
)

func newComputeService(t *testing.T, transport *recordingTransport, infos ...types.DatasourceInfo) *service {
	t.Helper()

	return testRoutingService(t, transport, []proxy.ClientRoute{{
		Name: "hosted",
		Client: &routingProxyClient{
			url:     "https://hosted.proxy",
			token:   "hosted-token",
			compute: infos,
		},
	}})
}

func callComputeOp(t *testing.T, svc *service, operationID string, args map[string]any) *httptest.ResponseRecorder {
	t.Helper()

	body, err := json.Marshal(map[string]any{"args": args})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/runtime/operations/"+operationID, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	handled := svc.handleComputeOperation(operationID, rec, req)
	require.True(t, handled, "operation %s not handled", operationID)

	return rec
}

// TestComputeForwardsIdempotencyKey is a regression test: the generated client
// sets the idempotency key as a request header, and the proxy doer must forward
// it. Dropping it makes every mutating compute op fail upstream with
// missing_idempotency_key.
func TestComputeForwardsIdempotencyKey(t *testing.T) {
	t.Parallel()

	transport := &recordingTransport{status: http.StatusAccepted, body: `{"operation_id":"op-1"}`, contentType: "application/json"}
	svc := newComputeService(t, transport, types.DatasourceInfo{Name: "production"})

	rec := callComputeOp(t, svc, "compute.create_sandbox", map[string]any{
		"template":        "ubuntu/24.04",
		"ttl":             "1h",
		"idempotency_key": "idem-123",
	})

	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	require.NotNil(t, transport.last)

	// The doer targets the /compute mount; the proxy strips it before the backend.
	assert.Equal(t, "/compute/v1/sandboxes", transport.last.URL.Path)
	assert.Equal(t, "production", transport.last.Header.Get(handlers.DatasourceHeader))
	assert.Equal(t, "idem-123", transport.last.Header.Get("Idempotency-Key"),
		"the idempotency key set by the generated client must be forwarded upstream")
	assert.JSONEq(t, `{"template":"ubuntu/24.04","ttl":"1h"}`, string(transport.lastBody))
}

func TestComputePromoteImageBuildsRequest(t *testing.T) {
	t.Parallel()

	transport := &recordingTransport{status: http.StatusCreated, body: `{}`, contentType: "application/json"}
	svc := newComputeService(t, transport, types.DatasourceInfo{Name: "production"})

	rec := callComputeOp(t, svc, "compute.promote_image", map[string]any{
		"id":              "snap-1",
		"name":            "ubuntu-warm",
		"version":         "v2",
		"description":     "ready to boot",
		"replace":         true,
		"tags":            []any{"devnet", "warm"},
		"idempotency_key": "idem-promote",
	})

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.NotNil(t, transport.last)

	assert.Equal(t, http.MethodPost, transport.last.Method)
	assert.Equal(t, "/compute/v1/images/snap-1/promote", transport.last.URL.Path)
	assert.Equal(t, "idem-promote", transport.last.Header.Get("Idempotency-Key"))
	assert.JSONEq(t, `{
		"name": "ubuntu-warm",
		"version": "v2",
		"description": "ready to boot",
		"replace": true,
		"tags": ["devnet", "warm"]
	}`, string(transport.lastBody))
}

func TestComputePromoteImageRequiresName(t *testing.T) {
	t.Parallel()

	transport := &recordingTransport{status: http.StatusCreated, body: `{}`, contentType: "application/json"}
	svc := newComputeService(t, transport, types.DatasourceInfo{Name: "production"})

	rec := callComputeOp(t, svc, "compute.promote_image", map[string]any{
		"id": "snap-1",
	})

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "name is required")
	assert.Nil(t, transport.last, "no upstream request should be made without a target name")
}

func TestComputeExposePortBuildsRequest(t *testing.T) {
	t.Parallel()

	transport := &recordingTransport{status: http.StatusCreated, body: `{}`, contentType: "application/json"}
	svc := newComputeService(t, transport, types.DatasourceInfo{Name: "production"})

	rec := callComputeOp(t, svc, "compute.expose_port", map[string]any{
		"id":              "sb-1",
		"port":            8080,
		"name":            "api",
		"idempotency_key": "idem-expose",
	})

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.NotNil(t, transport.last)

	assert.Equal(t, http.MethodPost, transport.last.Method)
	assert.Equal(t, "/compute/v1/sandboxes/sb-1/ports", transport.last.URL.Path)
	assert.Equal(t, "idem-expose", transport.last.Header.Get("Idempotency-Key"))
	assert.JSONEq(t, `{"port": 8080, "name": "api"}`, string(transport.lastBody))
}

func TestComputeUnexposePortRequiresPort(t *testing.T) {
	t.Parallel()

	transport := &recordingTransport{status: http.StatusNoContent, contentType: "application/json"}
	svc := newComputeService(t, transport, types.DatasourceInfo{Name: "production"})

	rec := callComputeOp(t, svc, "compute.unexpose_port", map[string]any{
		"id": "sb-1",
	})

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "port is required")
	assert.Nil(t, transport.last, "no upstream request should be made without a port")
}

// TestComputePreservesUpstreamStatus verifies a 2xx upstream status is passed
// through rather than flattened to 200.
func TestComputePreservesUpstreamStatus(t *testing.T) {
	t.Parallel()

	transport := &recordingTransport{status: http.StatusNoContent, contentType: "application/json"}
	svc := newComputeService(t, transport, types.DatasourceInfo{Name: "production"})

	rec := callComputeOp(t, svc, "compute.delete_sandbox", map[string]any{"id": "sb-1"})

	assert.Equal(t, http.StatusNoContent, rec.Code)
	require.NotNil(t, transport.last)
	assert.Equal(t, http.MethodDelete, transport.last.Method)
	assert.Equal(t, "/compute/v1/sandboxes/sb-1", transport.last.URL.Path)
}

// TestComputeListBuildsQuery confirms pagination args become query parameters
// and a GET reaches the expected path.
func TestComputeListBuildsQuery(t *testing.T) {
	t.Parallel()

	transport := &recordingTransport{status: http.StatusOK, body: `{"items":[]}`, contentType: "application/json"}
	svc := newComputeService(t, transport, types.DatasourceInfo{Name: "production"})

	rec := callComputeOp(t, svc, "compute.list_sandboxes", map[string]any{
		"limit":  25,
		"cursor": "abc",
	})

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, transport.last)

	assert.Equal(t, http.MethodGet, transport.last.Method)
	assert.Equal(t, "/compute/v1/sandboxes", transport.last.URL.Path)

	query := transport.last.URL.Query()
	assert.Equal(t, "25", query.Get("limit"))
	assert.Equal(t, "abc", query.Get("cursor"))
}

// TestComputeListForwardsFilters confirms repeated --filter values reach the
// backend as repeated filter query parameters, where compute applies them.
func TestComputeListForwardsFilters(t *testing.T) {
	t.Parallel()

	transport := &recordingTransport{status: http.StatusOK, body: `{"items":[]}`, contentType: "application/json"}
	svc := newComputeService(t, transport, types.DatasourceInfo{Name: "production"})

	rec := callComputeOp(t, svc, "compute.list_sandboxes", map[string]any{
		"filter": []any{"status=running", "vcpu>=4"},
	})

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, transport.last)

	assert.Equal(t, []string{"status=running", "vcpu>=4"}, transport.last.URL.Query()["filter"])
}

// TestComputeForwardsStructuredUpstreamError verifies a JSON error object from
// the backend is forwarded verbatim, keeping structured fields like code and
// request_id visible to the CLI instead of nesting them in a wrapper.
func TestComputeForwardsStructuredUpstreamError(t *testing.T) {
	t.Parallel()

	upstream := `{"error":"guest unreachable","code":"guest_unreachable","request_id":"req-123"}`
	transport := &recordingTransport{status: http.StatusInternalServerError, body: upstream, contentType: "application/json"}
	svc := newComputeService(t, transport, types.DatasourceInfo{Name: "production"})

	rec := callComputeOp(t, svc, "compute.get_sandbox", map[string]any{"id": "sb-1"})

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.JSONEq(t, upstream, rec.Body.String())
}

// TestComputeWrapsNonJSONUpstreamError verifies non-JSON error bodies still get
// the standard {"error": ...} wrapper.
func TestComputeWrapsNonJSONUpstreamError(t *testing.T) {
	t.Parallel()

	transport := &recordingTransport{status: http.StatusBadGateway, body: "upstream exploded", contentType: "text/plain"}
	svc := newComputeService(t, transport, types.DatasourceInfo{Name: "production"})

	rec := callComputeOp(t, svc, "compute.get_sandbox", map[string]any{"id": "sb-1"})

	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.JSONEq(t, `{"error":"upstream exploded"}`, rec.Body.String())
}

// TestComputeRequiresDatasourceWhenAmbiguous verifies an explicit datasource is
// required when more than one compute datasource is advertised.
func TestComputeRequiresDatasourceWhenAmbiguous(t *testing.T) {
	t.Parallel()

	transport := &recordingTransport{status: http.StatusOK, body: `{}`}
	svc := newComputeService(t, transport,
		types.DatasourceInfo{Name: "production"},
		types.DatasourceInfo{Name: "staging"},
	)

	rec := callComputeOp(t, svc, "compute.list_sandboxes", map[string]any{})

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Nil(t, transport.last, "no upstream request should be made when the datasource is ambiguous")
}
