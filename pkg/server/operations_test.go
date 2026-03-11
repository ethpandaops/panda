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
	"time"

	"github.com/ethpandaops/cartographoor/pkg/discovery"
	"github.com/go-chi/chi/v5"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/cartographoor"
	"github.com/ethpandaops/panda/pkg/operations"
	"github.com/ethpandaops/panda/pkg/proxy"
	"github.com/ethpandaops/panda/pkg/types"
)

func TestHandleAPIOperationPassthroughs(t *testing.T) {
	t.Parallel()

	start := time.Date(2025, time.January, 2, 3, 4, 5, 0, time.UTC)
	end := start.Add(30 * time.Minute)

	testCases := []struct {
		name               string
		operationID        string
		payload            any
		expectedMethod     string
		expectedPath       string
		expectedQuery      url.Values
		expectedDatasource string
		expectedBody       string
		responseBody       string
		responseType       string
	}{
		{
			name:        "prometheus query range",
			operationID: "prometheus.query_range",
			payload: operations.TypedRequest[operations.PrometheusRangeQueryArgs]{Args: operations.PrometheusRangeQueryArgs{
				Datasource: "metrics",
				Query:      "up",
				Start:      start.Format(time.RFC3339),
				End:        end.Format(time.RFC3339),
				Step:       "1m",
			}},
			expectedMethod: "GET",
			expectedPath:   "/prometheus/api/v1/query_range",
			expectedQuery: url.Values{
				"query": []string{"up"},
				"start": []string{"1735787045"},
				"end":   []string{"1735788845"},
				"step":  []string{"60"},
			},
			expectedDatasource: "metrics",
			responseBody:       `{"status":"success","data":{"resultType":"vector","result":[]}}`,
			responseType:       "application/json",
		},
		{
			name:        "loki label values",
			operationID: "loki.get_label_values",
			payload: operations.TypedRequest[operations.LokiLabelValuesArgs]{Args: operations.LokiLabelValuesArgs{
				Datasource: "logs",
				Label:      "job",
				Start:      start.Format(time.RFC3339),
				End:        end.Format(time.RFC3339),
			}},
			expectedMethod: "GET",
			expectedPath:   "/loki/loki/api/v1/label/job/values",
			expectedQuery: url.Values{
				"start": []string{"1735787045000000000"},
				"end":   []string{"1735788845000000000"},
			},
			expectedDatasource: "logs",
			responseBody:       `{"data":["node","validator"]}`,
			responseType:       "application/json",
		},
		{
			name:        "loki instant query defaults",
			operationID: "loki.query_instant",
			payload: operations.TypedRequest[operations.LokiInstantQueryArgs]{Args: operations.LokiInstantQueryArgs{
				Datasource: "logs",
				Query:      `{job="node"}`,
				Time:       start.Format(time.RFC3339),
			}},
			expectedMethod: "GET",
			expectedPath:   "/loki/loki/api/v1/query",
			expectedQuery: url.Values{
				"query":     []string{`{job="node"}`},
				"limit":     []string{"100"},
				"direction": []string{"backward"},
				"time":      []string{"1735787045000000000"},
			},
			expectedDatasource: "logs",
			responseBody:       `{"data":{"result":[]}}`,
			responseType:       "application/json",
		},
		{
			name:        "ethnode execution rpc",
			operationID: "ethnode.execution_rpc",
			payload: operations.TypedRequest[operations.EthNodeExecutionRPCArgs]{Args: operations.EthNodeExecutionRPCArgs{
				Network:  "mainnet",
				Instance: "lighthouse-geth-1",
				Method:   "eth_blockNumber",
				Params:   []any{"latest", false},
			}},
			expectedMethod: "POST",
			expectedPath:   "/execution/mainnet/lighthouse-geth-1/",
			expectedQuery:  url.Values{},
			expectedBody:   `{"id":1,"jsonrpc":"2.0","method":"eth_blockNumber","params":["latest",false]}`,
			responseBody:   `{"jsonrpc":"2.0","id":1,"result":"0x10"}`,
			responseType:   "application/json",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Helper()

				assert.Equal(t, tc.expectedMethod, r.Method)
				assert.Equal(t, tc.expectedPath, r.URL.Path)
				assert.Equal(t, tc.expectedQuery.Encode(), r.URL.Query().Encode())
				if tc.expectedDatasource != "" {
					assert.Equal(t, tc.expectedDatasource, r.Header.Get(proxy.DatasourceHeader))
				}

				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				if tc.expectedBody != "" {
					assert.JSONEq(t, tc.expectedBody, string(body))
				} else {
					assert.Empty(t, string(body))
				}

				w.Header().Set("Content-Type", tc.responseType)
				_, _ = w.Write([]byte(tc.responseBody))
			}))
			defer upstream.Close()

			srv := newOperationTestService(
				&stubProxyService{url: upstream.URL},
				nil,
				upstream.Client(),
			)

			rec := performOperationRequest(t, srv, tc.operationID, tc.payload)
			require.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, "passthrough", rec.Header().Get("X-Operation-Transport"))
			assert.Equal(t, tc.responseType, rec.Header().Get("Content-Type"))
			assert.JSONEq(t, tc.responseBody, rec.Body.String())
		})
	}
}

func TestHandleAPIOperationObjectResponses(t *testing.T) {
	t.Parallel()

	t.Run("ethnode health wraps status code", func(t *testing.T) {
		t.Parallel()

		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "GET", r.Method)
			assert.Equal(t, "/beacon/mainnet/lighthouse-geth-1/eth/v1/node/health", r.URL.Path)
			w.WriteHeader(http.StatusPartialContent)
		}))
		defer upstream.Close()

		srv := newOperationTestService(
			&stubProxyService{url: upstream.URL},
			nil,
			upstream.Client(),
		)

		rec := performOperationRequest(
			t,
			srv,
			"ethnode.get_node_health",
			operations.TypedRequest[operations.EthNodeNodeArgs]{Args: operations.EthNodeNodeArgs{
				Network:  "mainnet",
				Instance: "lighthouse-geth-1",
			}},
		)
		require.Equal(t, http.StatusOK, rec.Code)

		var response operations.Response
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))

		payload, err := operations.DecodeResponseData[operations.StatusCodePayload](&response)
		require.NoError(t, err)
		assert.Equal(t, http.StatusPartialContent, payload.StatusCode)
	})

	t.Run("prometheus upstream failures preserve operation context", func(t *testing.T) {
		t.Parallel()

		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/prometheus/api/v1/labels", r.URL.Path)
			assert.Equal(t, "metrics", r.Header.Get(proxy.DatasourceHeader))
			http.Error(w, "upstream unavailable", http.StatusServiceUnavailable)
		}))
		defer upstream.Close()

		srv := newOperationTestService(
			&stubProxyService{url: upstream.URL},
			nil,
			upstream.Client(),
		)

		rec := performOperationRequest(
			t,
			srv,
			"prometheus.get_labels",
			operations.TypedRequest[operations.DatasourceArgs]{Args: operations.DatasourceArgs{
				Datasource: "metrics",
			}},
		)
		require.Equal(t, http.StatusServiceUnavailable, rec.Code)
		assert.Contains(t, rec.Body.String(), "prometheus.get_labels upstream failure")
		assert.Contains(t, rec.Body.String(), "datasource=metrics")
	})

	t.Run("dora link returns typed url payload", func(t *testing.T) {
		t.Parallel()

		srv := newOperationTestService(
			&stubProxyService{url: "http://proxy.test"},
			&stubCartographoorClient{
				activeNetworks: map[string]discovery.Network{
					"hoodi": {
						ServiceURLs: &discovery.ServiceURLs{
							Dora: "https://dora.example",
						},
					},
				},
			},
			http.DefaultClient,
		)

		rec := performOperationRequest(
			t,
			srv,
			"dora.link_slot",
			operations.TypedRequest[operations.DoraSlotOrHashArgs]{Args: operations.DoraSlotOrHashArgs{
				Network:    "hoodi",
				SlotOrHash: "123",
			}},
		)
		require.Equal(t, http.StatusOK, rec.Code)

		var response operations.Response
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))

		payload, err := operations.DecodeResponseData[operations.URLPayload](&response)
		require.NoError(t, err)
		assert.Equal(t, "https://dora.example/slot/123", payload.URL)
	})

	t.Run("dora link rejects unknown networks", func(t *testing.T) {
		t.Parallel()

		srv := newOperationTestService(
			&stubProxyService{url: "http://proxy.test"},
			&stubCartographoorClient{
				activeNetworks: map[string]discovery.Network{
					"hoodi": {
						ServiceURLs: &discovery.ServiceURLs{
							Dora: "https://dora.example",
						},
					},
				},
			},
			http.DefaultClient,
		)

		rec := performOperationRequest(
			t,
			srv,
			"dora.link_slot",
			operations.TypedRequest[operations.DoraSlotOrHashArgs]{Args: operations.DoraSlotOrHashArgs{
				Network:    "unknown",
				SlotOrHash: "123",
			}},
		)
		require.Equal(t, http.StatusNotFound, rec.Code)
		assert.Contains(t, rec.Body.String(), `unknown network "unknown"`)
	})
}

func newOperationTestService(
	proxyService *stubProxyService,
	carto cartographoor.CartographoorClient,
	httpClient *http.Client,
) *service {
	srv := &service{
		log:                 logrus.New(),
		proxyService:        proxyService,
		cartographoorClient: carto,
		httpClient:          httpClient,
		operationHandlers:   make(map[string]operationHandler, 32),
	}
	srv.registerOperations()

	return srv
}

func performOperationRequest(
	t *testing.T,
	srv *service,
	operationID string,
	payload any,
) *httptest.ResponseRecorder {
	t.Helper()

	body, err := json.Marshal(payload)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/operations/"+operationID, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("operationID", operationID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))

	rec := httptest.NewRecorder()
	srv.handleAPIOperation(rec, req)

	return rec
}

type stubProxyService struct {
	url string
}

func (s *stubProxyService) Start(context.Context) error          { return nil }
func (s *stubProxyService) Stop(context.Context) error           { return nil }
func (s *stubProxyService) URL() string                          { return s.url }
func (s *stubProxyService) AuthorizeRequest(*http.Request) error { return nil }
func (s *stubProxyService) RegisterToken(string) string          { return "none" }
func (s *stubProxyService) RevokeToken(string)                   {}
func (s *stubProxyService) ClickHouseDatasources() []string      { return nil }
func (s *stubProxyService) ClickHouseDatasourceInfo() []types.DatasourceInfo {
	return nil
}
func (s *stubProxyService) PrometheusDatasources() []string { return nil }
func (s *stubProxyService) PrometheusDatasourceInfo() []types.DatasourceInfo {
	return nil
}
func (s *stubProxyService) LokiDatasources() []string { return nil }
func (s *stubProxyService) LokiDatasourceInfo() []types.DatasourceInfo {
	return nil
}
func (s *stubProxyService) S3Bucket() string                       { return "" }
func (s *stubProxyService) S3PublicURLPrefix() string              { return "" }
func (s *stubProxyService) EthNodeAvailable() bool                 { return true }
func (s *stubProxyService) DatasourceInfo() []types.DatasourceInfo { return nil }

type stubCartographoorClient struct {
	activeNetworks map[string]discovery.Network
}

func (s *stubCartographoorClient) Start(context.Context) error { return nil }
func (s *stubCartographoorClient) Stop() error                 { return nil }
func (s *stubCartographoorClient) GetAllNetworks() map[string]discovery.Network {
	return s.activeNetworks
}
func (s *stubCartographoorClient) GetActiveNetworks() map[string]discovery.Network {
	return s.activeNetworks
}
func (s *stubCartographoorClient) GetNetwork(name string) (discovery.Network, bool) {
	network, ok := s.activeNetworks[name]
	return network, ok
}
func (s *stubCartographoorClient) GetGroup(string) (map[string]discovery.Network, bool) {
	return nil, false
}
func (s *stubCartographoorClient) GetGroups() []string                    { return nil }
func (s *stubCartographoorClient) IsDevnet(discovery.Network) bool        { return false }
func (s *stubCartographoorClient) GetClusters(discovery.Network) []string { return nil }
