package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/operations"
	"github.com/ethpandaops/panda/pkg/proxy"
	"github.com/ethpandaops/panda/pkg/types"
)

func TestEthNodeListOperationsReturnUnavailableWhenEthNodeUnavailable(t *testing.T) {
	t.Parallel()

	for _, operationID := range []string{"ethnode.list_datasources", "ethnode.list_networks"} {
		t.Run(operationID, func(t *testing.T) {
			t.Parallel()

			svc := newEthNodeOperationService(false)
			rec := httptest.NewRecorder()

			handled := svc.handleEthNodeOperation(operationID, rec, newEthNodeOpRequest(t))
			require.True(t, handled)
			require.Equal(t, http.StatusServiceUnavailable, rec.Code)
			assert.Contains(t, rec.Body.String(), "Ethnode is not enabled or no node access is available.")
		})
	}
}

func TestEthNodeListNetworksRequiresCartographoorWhenEthNodeAvailable(t *testing.T) {
	t.Parallel()

	svc := newEthNodeOperationService(true)
	rec := httptest.NewRecorder()

	handled := svc.handleEthNodeOperation("ethnode.list_networks", rec, newEthNodeOpRequest(t))
	require.True(t, handled)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), "ethnode network discovery is unavailable")
}

func newEthNodeOperationService(ethnodeAvailable bool) *service {
	log := logrus.New()
	log.SetOutput(io.Discard)

	return &service{
		log:          log,
		proxyService: &ethNodeOperationProxy{ethnodeAvailable: ethnodeAvailable},
	}
}

func newEthNodeOpRequest(t *testing.T) *http.Request {
	t.Helper()

	return newEthNodeOpRequestWithArgs(t, nil)
}

func newEthNodeOpRequestWithArgs(t *testing.T, args map[string]any) *http.Request {
	t.Helper()

	body, err := json.Marshal(operations.Request{Args: args})
	require.NoError(t, err)

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/runtime/operations/ethnode",
		io.NopCloser(bytes.NewReader(body)),
	)
	req.Header.Set("Content-Type", "application/json")

	return req
}

type ethNodeOperationProxy struct {
	ethnodeAvailable bool
}

func (p *ethNodeOperationProxy) Start(_ context.Context) error { return nil }
func (p *ethNodeOperationProxy) Stop(_ context.Context) error  { return nil }
func (p *ethNodeOperationProxy) URL() string                   { return "" }
func (p *ethNodeOperationProxy) RegisterToken() string         { return proxy.NoAuthToken }
func (p *ethNodeOperationProxy) RevokeToken()                  {}
func (p *ethNodeOperationProxy) ClickHouseDatasources() []string {
	return nil
}
func (p *ethNodeOperationProxy) ClickHouseDatasourceInfo() []types.DatasourceInfo {
	return nil
}
func (p *ethNodeOperationProxy) ClickHouseQuery(_ context.Context, _, _ string, _ url.Values) ([]byte, error) {
	return nil, nil
}
func (p *ethNodeOperationProxy) PrometheusDatasourceInfo() []types.DatasourceInfo {
	return nil
}
func (p *ethNodeOperationProxy) LokiDatasourceInfo() []types.DatasourceInfo {
	return nil
}
func (p *ethNodeOperationProxy) EthNodeAvailable() bool { return p.ethnodeAvailable }
func (p *ethNodeOperationProxy) EthNodeDatasourceInfo() []types.DatasourceInfo {
	if !p.ethnodeAvailable {
		return nil
	}

	return []types.DatasourceInfo{{Type: "ethnode", Name: "ethnode"}}
}
func (p *ethNodeOperationProxy) EmbeddingAvailable() bool { return false }
func (p *ethNodeOperationProxy) EmbeddingModel() string   { return "" }
