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
	"github.com/ethpandaops/panda/pkg/serverapi"
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

func TestDoraHelperFlows(t *testing.T) {
	t.Parallel()

	t.Run("decodes identifier args and multiplies epochs", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(
			http.MethodPost,
			"/api/v1/operations/dora.link_slot",
			bytes.NewReader(mustJSON(t, operations.TypedRequest[operations.DoraSlotOrHashArgs]{
				Args: operations.DoraSlotOrHashArgs{Network: "hoodi", SlotOrHash: "123"},
			})),
		)

		network, identifier, err := decodeDoraIdentifierArgs(req, "slot_or_hash")
		require.NoError(t, err)
		assert.Equal(t, "hoodi", network)
		assert.Equal(t, "123", identifier)
		assert.EqualValues(t, 64, multiplyEpoch(float64(2)))
		assert.EqualValues(t, 96, multiplyEpoch(json.Number("3")))
		assert.EqualValues(t, 128, multiplyEpoch("4"))
		assert.Equal(t, "unknown", multiplyEpoch("unknown"))
	})

	t.Run("overview falls back to latest epoch and validators passthrough keeps filters", func(t *testing.T) {
		t.Parallel()

		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/v1/epoch/head":
				http.Error(w, "head unavailable", http.StatusServiceUnavailable)
			case "/api/v1/epoch/latest":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"data":{"epoch":"5","finalized":true,"globalparticipationrate":0.98,"validatorinfo":{"active":10,"total":12,"pending":1,"exited":1}}}`))
			case "/api/v1/validators":
				assert.Equal(t, "250", r.URL.Query().Get("limit"))
				assert.Equal(t, "active", r.URL.Query().Get("status"))
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"data":[]}`))
			default:
				t.Fatalf("unexpected Dora path %q", r.URL.Path)
			}
		}))
		defer upstream.Close()

		srv := newOperationTestService(
			&stubProxyService{url: "http://proxy.test"},
			&stubCartographoorClient{
				activeNetworks: map[string]discovery.Network{
					"hoodi": {ServiceURLs: &discovery.ServiceURLs{Dora: upstream.URL}},
				},
			},
			upstream.Client(),
		)

		rec := performOperationRequest(
			t,
			srv,
			"dora.get_network_overview",
			operations.TypedRequest[operations.DoraNetworkArgs]{Args: operations.DoraNetworkArgs{Network: "hoodi"}},
		)
		require.Equal(t, http.StatusOK, rec.Code)

		var overviewResponse operations.Response
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &overviewResponse))

		overview, err := operations.DecodeResponseData[operations.DoraOverviewPayload](&overviewResponse)
		require.NoError(t, err)
		assert.Equal(t, "5", overview.CurrentEpoch)
		assert.EqualValues(t, 160, overview.CurrentSlot)
		assert.EqualValues(t, 10, overview.ActiveValidatorCount)

		rec = performOperationRequest(
			t,
			srv,
			"dora.get_validators",
			operations.Request{Args: map[string]any{
				"network": "hoodi",
				"limit":   250,
				"status":  "active",
			}},
		)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.JSONEq(t, `{"data":[]}`, rec.Body.String())
	})
}

func TestEthNodeHelperFlows(t *testing.T) {
	t.Parallel()

	t.Run("validates helper inputs", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(
			http.MethodPost,
			"/api/v1/operations/ethnode.get_node_health",
			bytes.NewReader(mustJSON(t, operations.TypedRequest[operations.EthNodeNodeArgs]{
				Args: operations.EthNodeNodeArgs{Network: "mainnet", Instance: "beacon-1"},
			})),
		)

		nodeArgs, err := decodeValidatedEthNodeNodeArgs(req)
		require.NoError(t, err)
		assert.Equal(t, "mainnet", nodeArgs.Network)
		assert.Equal(t, "/eth/v1/node/health", ethNodeMeta(nodeArgs, "/eth/v1/node/health")["path"])

		path, err := parseEthNodePathArg(map[string]any{"path": "eth/v1/node/version"})
		require.NoError(t, err)
		assert.Equal(t, "/eth/v1/node/version", path)

		_, err = validateEthNodeNodeRequest("mainnet", "bad_instance!")
		require.Error(t, err)

		blockPayload, err := ethNodeHexPayload("block_number", "0x10", 16)
		require.NoError(t, err)
		assert.Equal(t, uint64(16), blockPayload.(operations.EthNodeBlockNumberPayload).BlockNumber)

		_, err = ethNodeHexPayload("unsupported", "0x1", 1)
		require.Error(t, err)
	})

	t.Run("handles curated beacon and rpc object wrappers", func(t *testing.T) {
		t.Parallel()

		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/beacon/mainnet/beacon-1/eth/v1/beacon/headers/head":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"data":{"root":"0xabc","header":{"message":{"slot":"123","proposer_index":"1","parent_root":"0xdef","state_root":"0xghi","body_root":"0xjkl"}}}}`))
			case "/beacon/mainnet/beacon-1/eth/v1/beacon/states/head/finality_checkpoints":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"data":{"finalized":{"epoch":"100"},"current_justified":{"epoch":"101"},"previous_justified":{"epoch":"99"}}}`))
			case "/execution/mainnet/execution-1/":
				var payload map[string]any
				require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
				switch payload["method"] {
				case "eth_blockNumber":
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x10"}`))
				case "web3_clientVersion":
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"reth/v1"}`))
				default:
					t.Fatalf("unexpected execution rpc method %v", payload["method"])
				}
			default:
				t.Fatalf("unexpected ethnode path %q", r.URL.Path)
			}
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
			"ethnode.get_beacon_headers",
			operations.TypedRequest[operations.EthNodeBeaconHeadersArgs]{Args: operations.EthNodeBeaconHeadersArgs{
				Network:  "mainnet",
				Instance: "beacon-1",
			}},
		)
		require.Equal(t, http.StatusOK, rec.Code)

		var headerResponse operations.Response
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &headerResponse))
		headerPayload, err := operations.DecodeResponseData[operations.EthNodeHeaderPayload](&headerResponse)
		require.NoError(t, err)
		assert.Equal(t, "123", headerPayload.Data.Header.Message.Slot)

		rec = performOperationRequest(
			t,
			srv,
			"ethnode.get_finality_checkpoints",
			operations.TypedRequest[operations.EthNodeFinalityArgs]{Args: operations.EthNodeFinalityArgs{
				Network:  "mainnet",
				Instance: "beacon-1",
			}},
		)
		require.Equal(t, http.StatusOK, rec.Code)

		var finalityResponse operations.Response
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &finalityResponse))
		finalityPayload, err := operations.DecodeResponseData[operations.EthNodeFinalityPayload](&finalityResponse)
		require.NoError(t, err)
		assert.Equal(t, "100", finalityPayload.Data.Finalized.Epoch)

		rec = performOperationRequest(
			t,
			srv,
			"ethnode.eth_block_number",
			operations.TypedRequest[operations.EthNodeNodeArgs]{Args: operations.EthNodeNodeArgs{
				Network:  "mainnet",
				Instance: "execution-1",
			}},
		)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), `"block_number":16`)

		rec = performOperationRequest(
			t,
			srv,
			"ethnode.web3_client_version",
			operations.TypedRequest[operations.EthNodeNodeArgs]{Args: operations.EthNodeNodeArgs{
				Network:  "mainnet",
				Instance: "execution-1",
			}},
		)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), `"reth/v1"`)
	})
}

func newOperationTestService(
	proxyService proxy.Service,
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

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()

	data, err := json.Marshal(value)
	require.NoError(t, err)

	return data
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
func (s *stubProxyService) S3Bucket() string          { return "" }
func (s *stubProxyService) S3PublicURLPrefix() string { return "" }
func (s *stubProxyService) EthNodeAvailable() bool    { return true }
func (s *stubProxyService) DatasourceInfo() []types.DatasourceInfo {
	return []types.DatasourceInfo{{Type: "ethnode", Name: "ethnode"}}
}
func (s *stubProxyService) Datasources() serverapi.DatasourcesResponse {
	return serverapi.DatasourcesResponse{
		Datasources:      s.DatasourceInfo(),
		EthNodeAvailable: true,
	}
}

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
