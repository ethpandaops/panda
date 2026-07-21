package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/proxy"
	"github.com/ethpandaops/panda/pkg/proxy/handlers"
	"github.com/ethpandaops/panda/pkg/types"
)

// testComputeSpec is a stand-in interface document as published by a compute
// upstream. Its free-text fields carry the brandmark-xyzzy sentinel so tests
// can prove upstream branding never reaches callers.
const testComputeSpec = `
openapi: 3.0.3
info:
  title: Brandmark-Xyzzy Fabric API
  description: brandmark-xyzzy internal control plane.
  version: v1
paths:
  /v1/sandboxes:
    get:
      operationId: listSandboxes
      description: brandmark-xyzzy sandbox listing.
      parameters:
        - $ref: '#/components/parameters/Limit'
        - $ref: '#/components/parameters/Cursor'
        - $ref: '#/components/parameters/Offset'
        - $ref: '#/components/parameters/Filter'
      responses:
        '200':
          description: OK
    post:
      operationId: createSandbox
      parameters:
        - $ref: '#/components/parameters/IdempotencyKey'
      requestBody:
        required: true
        content:
          application/json:
            schema:
              $ref: '#/components/schemas/CreateSandboxRequest'
      responses:
        '202':
          description: Accepted
  /v1/sandboxes/{id}:
    parameters:
      - $ref: '#/components/parameters/SandboxID'
    delete:
      operationId: deleteSandbox
      parameters:
        - $ref: '#/components/parameters/IdempotencyKey'
        - name: retain
          in: query
          required: false
          schema:
            type: boolean
      responses:
        '202':
          description: Accepted
  /v1/sandboxes/{id}/restart:
    parameters:
      - $ref: '#/components/parameters/SandboxID'
    post:
      operationId: restartSandbox
      parameters:
        - $ref: '#/components/parameters/IdempotencyKey'
      responses:
        '202':
          description: Accepted
  /v1/sandboxes/{id}/fork:
    parameters:
      - $ref: '#/components/parameters/SandboxID'
    post:
      operationId: forkSandbox
      parameters:
        - $ref: '#/components/parameters/IdempotencyKey'
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              required: [count, identity]
              properties:
                count:
                  type: integer
                identity:
                  type: object
      responses:
        '202':
          description: Accepted
  /v1/sandboxes/{id}/ports:
    parameters:
      - $ref: '#/components/parameters/SandboxID'
    post:
      operationId: exposePort
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              required: [port]
              properties:
                port:
                  type: integer
                name:
                  type: string
      responses:
        '200':
          description: OK
  /v1/sandboxes/{id}/ports/{port}:
    parameters:
      - $ref: '#/components/parameters/SandboxID'
      - name: port
        in: path
        required: true
        schema:
          type: integer
    delete:
      operationId: unexposePort
      responses:
        '204':
          description: Removed
  /v1/images/{id}/promote:
    parameters:
      - name: id
        in: path
        required: true
        schema:
          type: string
    post:
      operationId: promoteImage
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              required: [name]
              properties:
                name:
                  type: string
      responses:
        '201':
          description: Created
  /v1/me/ssh-keys:
    get:
      operationId: listSSHPublicKeys
      responses:
        '200':
          description: OK
components:
  parameters:
    SandboxID:
      name: id
      in: path
      required: true
      schema:
        type: string
    IdempotencyKey:
      name: Idempotency-Key
      in: header
      required: true
      schema:
        type: string
    Limit:
      name: limit
      in: query
      schema:
        type: integer
    Cursor:
      name: cursor
      in: query
      schema:
        type: string
    Offset:
      name: offset
      in: query
      schema:
        type: integer
    Filter:
      name: filter
      in: query
      schema:
        type: array
        items:
          type: string
  schemas:
    CreateSandboxRequest:
      type: object
      properties:
        template:
          type: string
        ttl:
          type: string
        source:
          type: object
`

// specServingTransport answers interface-document fetches itself and hands
// every other request to the recording transport, mirroring an upstream that
// publishes its own OpenAPI document.
type specServingTransport struct {
	inner *recordingTransport
	spec  string

	specFetches int
}

func (t *specServingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/compute/v1/openapi.yaml") {
		t.specFetches++

		resp := &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(t.spec)),
		}
		resp.Header.Set("Content-Type", "application/yaml")

		return resp, nil
	}

	return t.inner.RoundTrip(req)
}

func newComputeService(t *testing.T, transport *recordingTransport, infos ...types.DatasourceInfo) *service {
	t.Helper()

	return newComputeServiceWithTransport(t, &specServingTransport{inner: transport, spec: testComputeSpec}, infos...)
}

func newComputeServiceWithTransport(t *testing.T, transport http.RoundTripper, infos ...types.DatasourceInfo) *service {
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

// TestComputeForwardsIdempotencyKey is a regression test: the idempotency key
// travels as a request header, and dispatch must forward it. Dropping it makes
// every mutating compute op fail upstream with missing_idempotency_key.
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

	// Dispatch targets the /compute mount; the proxy strips it before the backend.
	assert.Equal(t, "/compute/v1/sandboxes", transport.last.URL.Path)
	assert.Equal(t, "production", transport.last.Header.Get(handlers.DatasourceHeader))
	assert.Equal(t, "idem-123", transport.last.Header.Get("Idempotency-Key"),
		"the idempotency key must be forwarded upstream as a header")
	assert.JSONEq(t, `{"template":"ubuntu/24.04","ttl":"1h"}`, string(transport.lastBody))
}

// TestComputeCreateSandboxFromSnapshot verifies the legacy flat snapshot
// arguments are adapted into the boot-source object the API expects.
func TestComputeCreateSandboxFromSnapshot(t *testing.T) {
	t.Parallel()

	transport := &recordingTransport{status: http.StatusAccepted, body: `{"operation_id":"op-2"}`, contentType: "application/json"}
	svc := newComputeService(t, transport, types.DatasourceInfo{Name: "production"})

	rec := callComputeOp(t, svc, "compute.create_sandbox", map[string]any{
		"snapshot_id":     "snap-7",
		"flavor":          "warm",
		"ttl":             "2h",
		"idempotency_key": "idem-snap",
	})

	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	require.NotNil(t, transport.last)

	assert.JSONEq(t, `{
		"source": {"kind": "snapshot", "snapshot_id": "snap-7", "flavor": "warm"},
		"ttl": "2h"
	}`, string(transport.lastBody))
}

// TestComputeForkNestsIdentity verifies the legacy flat identity arguments
// are adapted into the nested identity object.
func TestComputeForkNestsIdentity(t *testing.T) {
	t.Parallel()

	transport := &recordingTransport{status: http.StatusAccepted, body: `{"operation_id":"op-3"}`, contentType: "application/json"}
	svc := newComputeService(t, transport, types.DatasourceInfo{Name: "production"})

	rec := callComputeOp(t, svc, "compute.fork_sandbox", map[string]any{
		"id":              "sb-1",
		"count":           3,
		"identity_rng":    "reseed",
		"identity_clock":  "correct",
		"idempotency_key": "idem-fork",
	})

	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	require.NotNil(t, transport.last)

	assert.Equal(t, "/compute/v1/sandboxes/sb-1/fork", transport.last.URL.Path)
	assert.JSONEq(t, `{
		"count": 3,
		"identity": {"rng": "reseed", "clock": "correct"}
	}`, string(transport.lastBody))
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
		"offset": 0,
	})

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, transport.last)

	assert.Equal(t, http.MethodGet, transport.last.Method)
	assert.Equal(t, "/compute/v1/sandboxes", transport.last.URL.Path)

	query := transport.last.URL.Query()
	assert.Equal(t, "25", query.Get("limit"))
	assert.Equal(t, "abc", query.Get("cursor"))
	assert.False(t, query.Has("offset"), "zero-valued pagination args are treated as unset")
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

// TestComputeDynamicOperationFromSpec is the core property of runtime
// discovery: an operation that exists only in the upstream interface document
// is callable with no panda code that names it.
func TestComputeDynamicOperationFromSpec(t *testing.T) {
	t.Parallel()

	transport := &recordingTransport{status: http.StatusAccepted, body: `{"operation_id":"op-9"}`, contentType: "application/json"}
	svc := newComputeService(t, transport, types.DatasourceInfo{Name: "production"})

	rec := callComputeOp(t, svc, "compute.restart_sandbox", map[string]any{
		"id":              "sb-9",
		"idempotency_key": "idem-restart",
	})

	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	require.NotNil(t, transport.last)

	assert.Equal(t, http.MethodPost, transport.last.Method)
	assert.Equal(t, "/compute/v1/sandboxes/sb-9/restart", transport.last.URL.Path)
	assert.Equal(t, "idem-restart", transport.last.Header.Get("Idempotency-Key"))
}

// TestComputeLegacySSHKeyAlias keeps the operation names panda published
// before they were derived from the interface document working.
func TestComputeLegacySSHKeyAlias(t *testing.T) {
	t.Parallel()

	transport := &recordingTransport{status: http.StatusOK, body: `{"items":[]}`, contentType: "application/json"}
	svc := newComputeService(t, transport, types.DatasourceInfo{Name: "production"})

	rec := callComputeOp(t, svc, "compute.list_ssh_keys", map[string]any{})

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, transport.last)

	assert.Equal(t, http.MethodGet, transport.last.Method)
	assert.Equal(t, "/compute/v1/me/ssh-keys", transport.last.URL.Path)
}

func TestComputeUnknownOperationListsAvailable(t *testing.T) {
	t.Parallel()

	transport := &recordingTransport{status: http.StatusOK, body: `{}`, contentType: "application/json"}
	svc := newComputeService(t, transport, types.DatasourceInfo{Name: "production"})

	rec := callComputeOp(t, svc, "compute.explode_sandbox", map[string]any{})

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "unknown compute operation")
	assert.Contains(t, rec.Body.String(), "restart_sandbox")
}

func TestComputeRejectsUnknownArgsOnBodylessOperation(t *testing.T) {
	t.Parallel()

	transport := &recordingTransport{status: http.StatusNoContent, contentType: "application/json"}
	svc := newComputeService(t, transport, types.DatasourceInfo{Name: "production"})

	rec := callComputeOp(t, svc, "compute.delete_sandbox", map[string]any{
		"id":    "sb-1",
		"bogus": "value",
	})

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "does not accept")
	assert.Nil(t, transport.last, "no upstream request should be made with unknown arguments")
}

// TestComputeSpecFetchedOncePerDatasource verifies the interface document is
// cached rather than refetched per operation.
func TestComputeSpecFetchedOncePerDatasource(t *testing.T) {
	t.Parallel()

	inner := &recordingTransport{status: http.StatusOK, body: `{"items":[]}`, contentType: "application/json"}
	transport := &specServingTransport{inner: inner, spec: testComputeSpec}
	svc := newComputeServiceWithTransport(t, transport, types.DatasourceInfo{Name: "production"})

	for range 3 {
		rec := callComputeOp(t, svc, "compute.list_sandboxes", map[string]any{})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	}

	assert.Equal(t, 1, transport.specFetches)
}

// TestComputeCatalogHasNoUpstreamBranding verifies the discovered operation
// catalog carries structural data only: the upstream document's free text
// (title, descriptions) must never surface to callers.
func TestComputeCatalogHasNoUpstreamBranding(t *testing.T) {
	t.Parallel()

	transport := &recordingTransport{status: http.StatusOK, body: `{}`, contentType: "application/json"}
	svc := newComputeService(t, transport, types.DatasourceInfo{Name: "production"})

	rec := callComputeOp(t, svc, "compute.list_api_operations", map[string]any{})

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "restart_sandbox")
	assert.NotContains(t, strings.ToLower(rec.Body.String()), "xyzzy",
		"free text from the upstream interface document must not surface")
}

// TestComputeSpecFetchFailure verifies an unusable interface document fails
// the operation with an upstream error rather than a panic or a silent retry.
func TestComputeSpecFetchFailure(t *testing.T) {
	t.Parallel()

	inner := &recordingTransport{status: http.StatusOK, body: `{}`, contentType: "application/json"}
	transport := &specServingTransport{inner: inner, spec: "!! not a yaml document"}
	svc := newComputeServiceWithTransport(t, transport, types.DatasourceInfo{Name: "production"})

	rec := callComputeOp(t, svc, "compute.list_sandboxes", map[string]any{})

	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.Contains(t, rec.Body.String(), "compute interface")
}
