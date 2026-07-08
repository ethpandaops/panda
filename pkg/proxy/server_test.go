package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/auth"
	authclient "github.com/ethpandaops/panda/pkg/auth/client"
	"github.com/ethpandaops/panda/pkg/proxy/handlers"
)

func TestEmbeddingRoutesAreVersioned(t *testing.T) {
	t.Parallel()

	var inputTypes []string
	var inputTypesMu sync.Mutex
	embeddingAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[]}`))
		case "/v1/embeddings":
			var req openRouterRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode upstream request: %v", err)
			}

			inputTypesMu.Lock()
			inputTypes = append(inputTypes, req.InputType)
			inputTypesMu.Unlock()

			dims := req.Dimensions
			if dims == 0 {
				dims = 3
			}
			data := make([]openRouterEmbedding, 0, len(req.Input))
			for i := range req.Input {
				vec := make([]float32, dims)
				vec[0] = 1
				data = append(data, openRouterEmbedding{Index: i, Embedding: vec})
			}

			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(openRouterResponse{Data: data}); err != nil {
				t.Fatalf("encode upstream response: %v", err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(embeddingAPI.Close)

	cfg := ServerConfig{
		Auth: AuthConfig{Mode: AuthModeNone},
		ClickHouse: []ClickHouseClusterConfig{{
			BaseDatasourceConfig: BaseDatasourceConfig{Name: "clickhouse"},
			Host:                 "example.com",
			Port:                 8123,
			Username:             "user",
			Password:             "pass",
		}},
		Embedding: &EmbeddingConfig{
			APIKey: "test-api-key",
			Model:  "openai/text-embedding-3-small",
			APIURL: embeddingAPI.URL + "/v1",
		},
		EmbeddingV2: &EmbeddingConfig{
			APIKey:     "test-api-key",
			Model:      "google/gemini-embedding-2",
			APIURL:     embeddingAPI.URL + "/v1",
			Dimensions: 3,
		},
	}
	cfg.ApplyDefaults()

	srv, err := newServer(logrus.New(), cfg, "http://proxy.test", "18081")
	if err != nil {
		t.Fatalf("newServer failed: %v", err)
	}

	postJSON := func(path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		srv.mux.ServeHTTP(rec, req)

		return rec
	}

	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/datasources", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/datasources status = %d, want %d", rec.Code, http.StatusOK)
	}

	var ds DatasourcesResponse
	if err := json.NewDecoder(rec.Body).Decode(&ds); err != nil {
		t.Fatalf("decode datasources: %v", err)
	}
	if !ds.EmbeddingAvailable {
		t.Fatal("EmbeddingAvailable = false, want true")
	}
	if ds.EmbeddingModel != "openai/text-embedding-3-small" {
		t.Fatalf("EmbeddingModel = %q, want v1 model", ds.EmbeddingModel)
	}

	rec = postJSON("/embedding", `{"items":[]}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("/embedding status = %d, want %d", rec.Code, http.StatusNotFound)
	}

	rec = postJSON("/embed", `{"items":[{"hash":"v1","text":"hello"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("/embed status = %d body=%q, want %d", rec.Code, rec.Body.String(), http.StatusOK)
	}

	var v1Resp map[string]json.RawMessage
	if err := json.NewDecoder(rec.Body).Decode(&v1Resp); err != nil {
		t.Fatalf("decode v1 response: %v", err)
	}
	if _, ok := v1Resp["dimensions"]; ok {
		t.Fatal("v1 response included dimensions")
	}

	rec = postJSON("/v2/embedding", `{"items":[{"hash":"v2","text":"hello"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("/v2/embedding status = %d body=%q, want %d", rec.Code, rec.Body.String(), http.StatusOK)
	}

	var v2Resp EmbedResponse
	if err := json.NewDecoder(rec.Body).Decode(&v2Resp); err != nil {
		t.Fatalf("decode v2 response: %v", err)
	}
	if v2Resp.Model != "google/gemini-embedding-2" || v2Resp.Dimensions != 3 {
		t.Fatalf("v2 response model/dims = %s/%d, want google/gemini-embedding-2/3", v2Resp.Model, v2Resp.Dimensions)
	}

	rec = postJSON("/v3/embedding", `{"items":[{"hash":"v3","text":"hello"}]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("/v3/embedding missing task status = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	rec = postJSON("/v3/embedding", `{"items":[{"hash":"v3","text":"hello"}],"task":"banana"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("/v3/embedding invalid task status = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	rec = postJSON("/v3/embedding", `{"items":[{"hash":"v3","text":"hello"}],"task":"query"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("/v3/embedding status = %d body=%q, want %d", rec.Code, rec.Body.String(), http.StatusOK)
	}

	rec = postJSON("/v2/embedding/check", `{"hashes":[]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("/v2/embedding/check status = %d body=%q, want %d", rec.Code, rec.Body.String(), http.StatusOK)
	}

	var checkResp EmbedCheckResponse
	if err := json.NewDecoder(rec.Body).Decode(&checkResp); err != nil {
		t.Fatalf("decode v2 check response: %v", err)
	}
	if checkResp.Model != "google/gemini-embedding-2" || checkResp.Dimensions != 3 {
		t.Fatalf("v2 check model/dims = %s/%d, want google/gemini-embedding-2/3", checkResp.Model, checkResp.Dimensions)
	}

	rec = postJSON("/v3/embedding/check", `{"hashes":[]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("/v3/embedding/check missing task status = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	rec = postJSON("/v3/embedding/check", `{"hashes":[],"task":"banana"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("/v3/embedding/check invalid task status = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	inputTypesMu.Lock()
	gotInputTypes := append([]string(nil), inputTypes...)
	inputTypesMu.Unlock()

	wantInputTypes := []string{"", "", "search_query"}
	if len(gotInputTypes) != len(wantInputTypes) {
		t.Fatalf("upstream input_types = %#v, want %#v", gotInputTypes, wantInputTypes)
	}
	for i := range wantInputTypes {
		if gotInputTypes[i] != wantInputTypes[i] {
			t.Fatalf("upstream input_types = %#v, want %#v", gotInputTypes, wantInputTypes)
		}
	}
}

func TestEmbeddingCheckRoutesRejectTooManyHashes(t *testing.T) {
	t.Parallel()

	embeddingAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(embeddingAPI.Close)

	cfg := ServerConfig{
		Auth: AuthConfig{Mode: AuthModeNone},
		ClickHouse: []ClickHouseClusterConfig{{
			BaseDatasourceConfig: BaseDatasourceConfig{Name: "clickhouse"},
			Host:                 "example.com",
			Port:                 8123,
			Username:             "user",
			Password:             "pass",
		}},
		Embedding: &EmbeddingConfig{
			APIKey: "test-api-key",
			Model:  "openai/text-embedding-3-small",
			APIURL: embeddingAPI.URL + "/v1",
		},
		EmbeddingV2: &EmbeddingConfig{
			APIKey:     "test-api-key",
			Model:      "google/gemini-embedding-2",
			APIURL:     embeddingAPI.URL + "/v1",
			Dimensions: 3,
		},
	}

	srv, err := newServer(logrus.New(), cfg, "http://proxy.test", "18081")
	if err != nil {
		t.Fatalf("newServer failed: %v", err)
	}

	hashes := make([]string, maxEmbedItems+1)
	for i := range hashes {
		hashes[i] = "hash-" + strconv.Itoa(i)
	}

	body, err := json.Marshal(EmbedCheckRequest{Hashes: hashes, Task: EmbedTaskQuery})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	for _, path := range []string{"/embed/check", "/v2/embedding/check", "/v3/embedding/check"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		srv.mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d body=%q, want %d", path, rec.Code, rec.Body.String(), http.StatusBadRequest)
		}
		if !strings.Contains(rec.Body.String(), "too many hashes: 501 exceeds maximum of 500") {
			t.Fatalf("%s body = %q, want too many hashes error", path, rec.Body.String())
		}
	}
}

func TestEmbeddingV1V2IgnoreTaskAndUseSymmetricCacheKeys(t *testing.T) {
	t.Parallel()

	var (
		inputTypes   []string
		inputTypesMu sync.Mutex
	)

	embeddingAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[]}`))
		case "/v1/embeddings":
			var req openRouterRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode upstream request: %v", err)
			}

			inputTypesMu.Lock()
			inputTypes = append(inputTypes, req.InputType)
			inputTypesMu.Unlock()

			dims := req.Dimensions
			if dims == 0 {
				dims = 3
			}

			data := make([]openRouterEmbedding, 0, len(req.Input))
			for i := range req.Input {
				vec := make([]float32, dims)
				vec[0] = 1
				if dims > 1 {
					vec[1] = 2
				}
				if dims > 2 {
					vec[2] = 3
				}
				data = append(data, openRouterEmbedding{Index: i, Embedding: vec})
			}

			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(openRouterResponse{Data: data}); err != nil {
				t.Fatalf("encode upstream response: %v", err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(embeddingAPI.Close)

	cfg := ServerConfig{
		Auth: AuthConfig{Mode: AuthModeNone},
		ClickHouse: []ClickHouseClusterConfig{{
			BaseDatasourceConfig: BaseDatasourceConfig{Name: "clickhouse"},
			Host:                 "example.com",
			Port:                 8123,
			Username:             "user",
			Password:             "pass",
		}},
		Embedding: &EmbeddingConfig{
			APIKey: "test-api-key",
			Model:  "v1-model",
			APIURL: embeddingAPI.URL + "/v1",
		},
		EmbeddingV2: &EmbeddingConfig{
			APIKey:     "test-api-key",
			Model:      "v2-model",
			APIURL:     embeddingAPI.URL + "/v1",
			Dimensions: 3,
		},
	}

	srv, err := newServer(logrus.New(), cfg, "http://proxy.test", "18081")
	if err != nil {
		t.Fatalf("newServer failed: %v", err)
	}

	postJSON := func(path string) *httptest.ResponseRecorder {
		body := `{"items":[{"hash":"same-hash","text":"hello"}],"task":"query"}`
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		srv.mux.ServeHTTP(rec, req)

		return rec
	}

	v1Rec := postJSON("/embed")
	if v1Rec.Code != http.StatusOK {
		t.Fatalf("/embed status = %d body=%q, want %d", v1Rec.Code, v1Rec.Body.String(), http.StatusOK)
	}

	v2Rec := postJSON("/v2/embedding")
	if v2Rec.Code != http.StatusOK {
		t.Fatalf("/v2/embedding status = %d body=%q, want %d", v2Rec.Code, v2Rec.Body.String(), http.StatusOK)
	}

	var v1Resp, v2Resp EmbedResponse
	if err := json.NewDecoder(v1Rec.Body).Decode(&v1Resp); err != nil {
		t.Fatalf("decode v1 response: %v", err)
	}
	if err := json.NewDecoder(v2Rec.Body).Decode(&v2Resp); err != nil {
		t.Fatalf("decode v2 response: %v", err)
	}
	if len(v1Resp.Results) != 1 || len(v2Resp.Results) != 1 {
		t.Fatalf("response result lengths = %d/%d, want 1/1", len(v1Resp.Results), len(v2Resp.Results))
	}
	if !float32SlicesEqual(v1Resp.Results[0].Vector, v2Resp.Results[0].Vector) {
		t.Fatalf("v1/v2 vectors = %v/%v, want symmetric result", v1Resp.Results[0].Vector, v2Resp.Results[0].Vector)
	}

	inputTypesMu.Lock()
	gotInputTypes := append([]string(nil), inputTypes...)
	inputTypesMu.Unlock()
	if len(gotInputTypes) != 2 || gotInputTypes[0] != "" || gotInputTypes[1] != "" {
		t.Fatalf("upstream input_types = %#v, want empty input_type for v1/v2", gotInputTypes)
	}

	ctx := context.Background()
	v1Cached, err := srv.embeddingService.cache.GetMulti(ctx, []string{
		"v1-model:same-hash",
		"v1-model:query:same-hash",
	})
	if err != nil {
		t.Fatalf("read v1 cache: %v", err)
	}
	if _, ok := v1Cached["v1-model:same-hash"]; !ok {
		t.Fatalf("v1 cache keys = %#v, want task-less key", v1Cached)
	}
	if _, ok := v1Cached["v1-model:query:same-hash"]; ok {
		t.Fatalf("v1 cache keys = %#v, did not expect task key", v1Cached)
	}

	v2Cached, err := srv.embeddingServiceV2.cache.GetMulti(ctx, []string{
		"v2-model:3:same-hash",
		"v2-model:3:query:same-hash",
	})
	if err != nil {
		t.Fatalf("read v2 cache: %v", err)
	}
	if _, ok := v2Cached["v2-model:3:same-hash"]; !ok {
		t.Fatalf("v2 cache keys = %#v, want task-less key", v2Cached)
	}
	if _, ok := v2Cached["v2-model:3:query:same-hash"]; ok {
		t.Fatalf("v2 cache keys = %#v, did not expect task key", v2Cached)
	}
}

func float32SlicesEqual(a, b []float32) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}

func TestRegisterRoutesMatchesClickHouseSubpaths(t *testing.T) {
	t.Parallel()

	cfg := ServerConfig{
		Auth: AuthConfig{Mode: AuthModeNone},
		ClickHouse: []ClickHouseClusterConfig{
			{
				BaseDatasourceConfig: BaseDatasourceConfig{Name: "clickhouse-raw"},
				Host:                 "example.com",
				Port:                 8123,
				Username:             "user",
				Password:             "pass",
			},
		},
	}
	cfg.ApplyDefaults()

	srv, err := newServer(logrus.New(), cfg, "http://proxy.test", "18081")
	if err != nil {
		t.Fatalf("newServer failed: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/clickhouse/query", nil)
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected clickhouse handler status %d, got %d", http.StatusBadRequest, rec.Code)
	}
}

func TestServerConfigAllowsAutodiscoverOnly(t *testing.T) {
	t.Parallel()

	cfg := ServerConfig{
		Auth: AuthConfig{Mode: AuthModeNone},
		ClickHouse: []ClickHouseClusterConfig{
			{
				BaseDatasourceConfig: BaseDatasourceConfig{
					Name:        "local-kurtosis",
					Description: "Local OTel datasource",
				},
				Host:         "127.0.0.1",
				Port:         18123,
				Database:     "otel",
				Autodiscover: true,
			},
		},
	}
	cfg.ApplyDefaults()

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}

	if got := cfg.ClickHouse[0].AutodiscoverInterval; got != defaultAutodiscoverInterval {
		t.Fatalf("Autodiscover interval = %v, want 10s", got)
	}

	srv, err := newServer(logrus.New(), cfg, "http://proxy.test", "18081")
	if err != nil {
		t.Fatalf("newServer failed: %v", err)
	}

	if srv.clickhouseHandler == nil {
		t.Fatal("expected clickhouse handler for autodiscover-only config")
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/clickhouse/query", nil)
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected clickhouse route status %d, got %d", http.StatusBadRequest, rec.Code)
	}
}

func TestServerConfigRejectsAutodiscoverMissingDatabase(t *testing.T) {
	t.Parallel()

	cfg := ServerConfig{
		Auth: AuthConfig{Mode: AuthModeNone},
		ClickHouse: []ClickHouseClusterConfig{
			{
				BaseDatasourceConfig: BaseDatasourceConfig{Name: "local-kurtosis"},
				Host:                 "127.0.0.1",
				Port:                 18123,
				Autodiscover:         true,
			},
		},
	}
	cfg.ApplyDefaults()

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want missing database error")
	}
	if !strings.Contains(err.Error(), "clickhouse[0].database is required when autodiscover is enabled") {
		t.Fatalf("Validate() error = %q, want missing database error", err)
	}
}

func TestServerConfigRejectsNegativeAutodiscoverInterval(t *testing.T) {
	t.Parallel()

	cfg := ServerConfig{
		Auth: AuthConfig{Mode: AuthModeNone},
		ClickHouse: []ClickHouseClusterConfig{
			{
				BaseDatasourceConfig: BaseDatasourceConfig{Name: "local-kurtosis"},
				Host:                 "127.0.0.1",
				Port:                 18123,
				Database:             "otel",
				Autodiscover:         true,
				AutodiscoverInterval: -1 * time.Second,
			},
		},
	}
	cfg.ApplyDefaults()

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want negative interval error")
	}
	if !strings.Contains(err.Error(), "clickhouse[0].autodiscover_interval cannot be negative") {
		t.Fatalf("Validate() error = %q, want negative interval error", err)
	}
}

func TestAutodiscoverCannotManageStaticClickHouseDatasource(t *testing.T) {
	t.Parallel()

	cfg := ServerConfig{
		Auth: AuthConfig{Mode: AuthModeNone},
		ClickHouse: []ClickHouseClusterConfig{
			{
				BaseDatasourceConfig: BaseDatasourceConfig{
					Name:        "local-kurtosis",
					Description: "Static datasource",
				},
				Host:     "static.example",
				Port:     8123,
				Database: "staticdb",
			},
			{
				BaseDatasourceConfig: BaseDatasourceConfig{Name: "local-kurtosis"},
				Host:                 "127.0.0.1",
				Port:                 18123,
				Database:             "otel",
				Autodiscover:         true,
			},
		},
	}
	cfg.ApplyDefaults()

	srv, err := newServer(logrus.New(), cfg, "http://proxy.test", "18081")
	if err != nil {
		t.Fatalf("newServer failed: %v", err)
	}

	if added := srv.addAutodiscoveredClickHouseCluster(handlers.ClickHouseConfig{
		Name:        "local-kurtosis",
		Description: "Dynamic datasource",
		Host:        "dynamic.example",
		Port:        18123,
		Database:    "otel",
	}); added {
		t.Fatal("expected autodiscover add to be skipped for static datasource name")
	}

	cfgAfterAdd, ok := srv.clickhouseHandler.ClusterConfig("local-kurtosis")
	if !ok {
		t.Fatal("expected static local-kurtosis cluster to remain")
	}
	if cfgAfterAdd.Description != "Static datasource" || cfgAfterAdd.Database != "staticdb" {
		t.Fatalf("cluster after autodiscover add = %#v, want unchanged static config", cfgAfterAdd)
	}

	srv.removeAutodiscoveredClickHouseCluster("local-kurtosis")

	cfgAfterRemove, ok := srv.clickhouseHandler.ClusterConfig("local-kurtosis")
	if !ok {
		t.Fatal("expected static local-kurtosis cluster to remain after dynamic remove")
	}
	if cfgAfterRemove.Description != "Static datasource" || cfgAfterRemove.Database != "staticdb" {
		t.Fatalf("cluster after autodiscover remove = %#v, want unchanged static config", cfgAfterRemove)
	}
}

func TestAutodiscoverReconcilesClickHouseDatasource(t *testing.T) {
	t.Parallel()

	pingOK := true
	databaseExists := false
	fakeClickHouse := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ping":
			if !pingOK {
				_, _ = w.Write([]byte("Nope."))
				return
			}

			_, _ = w.Write([]byte("Ok."))
		case "/":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("reading request body: %v", err)
			}

			query := string(body)
			if !strings.Contains(query, "system.databases") {
				t.Fatalf("database probe query = %q, want system.databases", query)
			}
			if !strings.Contains(query, "'otel'") {
				t.Fatalf("database probe query = %q, want quoted otel database", query)
			}

			if !databaseExists {
				return
			}

			_, _ = w.Write([]byte("1\n"))
		default:
			t.Fatalf("unexpected autodiscover request path %q", r.URL.Path)
		}
	}))
	defer fakeClickHouse.Close()

	wantHost, wantPort := clickHouseAutodiscoverTestAddress(t, fakeClickHouse)

	cfg := ServerConfig{
		Auth: AuthConfig{Mode: AuthModeNone},
		ClickHouse: []ClickHouseClusterConfig{
			{
				BaseDatasourceConfig: BaseDatasourceConfig{
					Name:        "local-kurtosis",
					Description: "Custom local OTel datasource",
				},
				Host:                 wantHost,
				Port:                 wantPort,
				Database:             "otel",
				Autodiscover:         true,
				AutodiscoverInterval: time.Hour,
			},
		},
	}
	cfg.ApplyDefaults()

	srv, err := newServer(logrus.New(), cfg, "http://proxy.test", "18081")
	if err != nil {
		t.Fatalf("newServer failed: %v", err)
	}

	present := false
	entry := cfg.ClickHouse[0]

	srv.reconcileClickHouseAutodiscover(t.Context(), entry, &present)
	if present {
		t.Fatal("expected datasource to remain absent when database is missing")
	}
	if srv.clickhouseHandler.HasCluster("local-kurtosis") {
		t.Fatal("expected local-kurtosis cluster to be absent")
	}

	databaseExists = true
	srv.reconcileClickHouseAutodiscover(t.Context(), entry, &present)
	if !present {
		t.Fatal("expected datasource to become present")
	}
	if !srv.clickhouseHandler.HasCluster("local-kurtosis") {
		t.Fatal("expected local-kurtosis cluster to be present")
	}
	clusterCfg, ok := srv.clickhouseHandler.ClusterConfig("local-kurtosis")
	if !ok {
		t.Fatal("expected local-kurtosis cluster config to exist")
	}
	if clusterCfg.Host != wantHost || clusterCfg.Port != wantPort || clusterCfg.Database != "otel" ||
		clusterCfg.Description != "Custom local OTel datasource" {
		t.Fatalf("cluster config = %#v, want host=%q port=%d database=otel description=custom", clusterCfg, wantHost, wantPort)
	}

	info := srv.ClickHouseDatasourceInfo()
	if len(info) != 1 {
		t.Fatalf("ClickHouseDatasourceInfo() length = %d, want 1", len(info))
	}
	if info[0].Name != "local-kurtosis" {
		t.Fatalf("ClickHouseDatasourceInfo()[0].Name = %q, want local-kurtosis", info[0].Name)
	}
	if info[0].Description != "Custom local OTel datasource" {
		t.Fatalf("ClickHouseDatasourceInfo()[0].Description = %q, want custom description", info[0].Description)
	}
	if got := info[0].Metadata["database"]; got != "otel" {
		t.Fatalf("ClickHouseDatasourceInfo()[0].Metadata[database] = %q, want otel", got)
	}

	var datasources DatasourcesResponse
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/datasources", nil)
	srv.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/datasources status = %d, want %d", rec.Code, http.StatusOK)
	}
	if err := json.NewDecoder(rec.Body).Decode(&datasources); err != nil {
		t.Fatalf("decoding /datasources response: %v", err)
	}
	if got := datasourceNames(datasources.ClickHouseInfo); len(got) != 1 || got[0] != "local-kurtosis" {
		t.Fatalf("/datasources ClickHouseInfo names = %v, want [local-kurtosis]", got)
	}
	if len(datasources.ClickHouseInfo) != 1 || datasources.ClickHouseInfo[0].Metadata["database"] != "otel" {
		t.Fatalf("/datasources ClickHouseInfo = %v, want local-kurtosis metadata database=otel", datasources.ClickHouseInfo)
	}
	if datasources.ClickHouseInfo[0].Description != "Custom local OTel datasource" {
		t.Fatalf("/datasources ClickHouseInfo[0].Description = %q, want custom description", datasources.ClickHouseInfo[0].Description)
	}

	pingOK = false
	srv.reconcileClickHouseAutodiscover(t.Context(), entry, &present)
	if present {
		t.Fatal("expected datasource to become absent")
	}
	if srv.clickhouseHandler.HasCluster("local-kurtosis") {
		t.Fatal("expected local-kurtosis cluster to be removed")
	}
	if got := srv.ClickHouseDatasourceInfo(); len(got) != 0 {
		t.Fatalf("ClickHouseDatasourceInfo() after removal = %v, want empty", got)
	}

	pingOK = true
	srv.reconcileClickHouseAutodiscover(t.Context(), entry, &present)
	if !present {
		t.Fatal("expected datasource to become present again")
	}
	if !srv.clickhouseHandler.HasCluster("local-kurtosis") {
		t.Fatal("expected local-kurtosis cluster to be re-added")
	}
}

func clickHouseAutodiscoverTestAddress(t *testing.T, srv *httptest.Server) (string, int) {
	t.Helper()

	parsed, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parsing fake ClickHouse URL: %v", err)
	}

	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatalf("parsing fake ClickHouse port: %v", err)
	}

	return parsed.Hostname(), port
}

func TestURLFromEphemeralListenerAddr(t *testing.T) {
	t.Parallel()

	if !listenPortIsEphemeral("127.0.0.1:0") {
		t.Fatalf("listenPortIsEphemeral(127.0.0.1:0) = false, want true")
	}
	if listenPortIsEphemeral("127.0.0.1:18081") {
		t.Fatalf("listenPortIsEphemeral(127.0.0.1:18081) = true, want false")
	}

	addr := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 49321}
	if got := urlFromListenerAddr(addr, "http://localhost:0"); got != "http://127.0.0.1:49321" {
		t.Fatalf("urlFromListenerAddr() = %q, want actual listener URL", got)
	}
}

func TestMetricsDatasourceLabelUsesConfiguredNamesOnly(t *testing.T) {
	t.Parallel()

	cfg := ServerConfig{
		Auth: AuthConfig{Mode: AuthModeNone},
		ClickHouse: []ClickHouseClusterConfig{
			{BaseDatasourceConfig: BaseDatasourceConfig{Name: "clickhouse-raw"}, Host: "example.com", Port: 8123, Username: "user", Password: "pass"},
		},
		Prometheus: []PrometheusInstanceConfig{
			{BaseDatasourceConfig: BaseDatasourceConfig{Name: "prod"}, URL: "https://prom.example.com"},
		},
	}
	cfg.ApplyDefaults()

	srv, err := newServer(logrus.New(), cfg, "http://proxy.test", "18081")
	if err != nil {
		t.Fatalf("newServer failed: %v", err)
	}

	if got := srv.metricsDatasourceLabel("clickhouse", "clickhouse-raw"); got != "clickhouse-raw" {
		t.Fatalf("expected configured clickhouse datasource, got %q", got)
	}

	if got := srv.metricsDatasourceLabel("clickhouse", "attacker-"+t.Name()); got != "unknown" {
		t.Fatalf("expected unknown label for unconfigured datasource, got %q", got)
	}

	if got := srv.metricsDatasourceLabel("prometheus", ""); got != "default" {
		t.Fatalf("expected default label for empty datasource, got %q", got)
	}
}

func TestAuthMetadataEndpointReturnsConfig(t *testing.T) {
	t.Parallel()

	cfg := ServerConfig{
		Auth: AuthConfig{
			Mode: AuthModeOIDC,
			Issuers: []OIDCIssuerConfig{
				{IssuerURL: "https://dex.example.com", ClientID: "panda-proxy"},
			},
		},
		ClickHouse: []ClickHouseClusterConfig{
			{BaseDatasourceConfig: BaseDatasourceConfig{Name: "clickhouse-raw"}, Host: "example.com", Port: 8123, Username: "user", Password: "pass"},
		},
	}
	cfg.ApplyDefaults()

	srv, err := newServer(logrus.New(), cfg, "http://proxy.test", "18081")
	if err != nil {
		t.Fatalf("newServer failed: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/metadata", nil)
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	var got AuthMetadataResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	if !got.Enabled {
		t.Fatal("expected enabled=true")
	}

	if got.Mode != "oidc" {
		t.Fatalf("expected mode=oidc, got %q", got.Mode)
	}

	if got.IssuerURL != "https://dex.example.com" {
		t.Fatalf("expected issuer_url=https://dex.example.com, got %q", got.IssuerURL)
	}

	if got.ClientID != "panda-proxy" {
		t.Fatalf("expected client_id=panda-proxy, got %q", got.ClientID)
	}

	if len(got.Scopes) != 0 {
		t.Fatalf("expected no advertised scopes without a workflow engine, got %v", got.Scopes)
	}
}

func TestAuthMetadataEndpointAdvertisesWorkflowScope(t *testing.T) {
	t.Parallel()

	cfg := ServerConfig{
		Auth: AuthConfig{
			Mode: AuthModeOIDC,
			Issuers: []OIDCIssuerConfig{
				{IssuerURL: "https://authentik.example.com/application/o/panda-proxy/", ClientID: "panda-proxy"},
			},
		},
		Workflow: &WorkflowConfig{
			URL:      "https://workflows.example.com",
			AuthMode: WorkflowAuthModePassthrough,
		},
		ClickHouse: []ClickHouseClusterConfig{
			{BaseDatasourceConfig: BaseDatasourceConfig{Name: "clickhouse-raw"}, Host: "example.com", Port: 8123, Username: "user", Password: "pass"},
		},
	}
	cfg.ApplyDefaults()

	srv, err := newServer(logrus.New(), cfg, "http://proxy.test", "18081")
	if err != nil {
		t.Fatalf("newServer failed: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/metadata", nil)
	srv.mux.ServeHTTP(rec, req)

	var got AuthMetadataResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	// The complete request set: client defaults plus the workflow feature scope.
	want := append(append([]string(nil), authclient.DefaultScopes...), "workflows")
	if !reflect.DeepEqual(got.Scopes, want) {
		t.Fatalf("expected advertised scopes %v, got %v", want, got.Scopes)
	}
}

func TestAuthMetadataEndpointNoAuth(t *testing.T) {
	t.Parallel()

	cfg := ServerConfig{
		Auth: AuthConfig{Mode: AuthModeNone},
		ClickHouse: []ClickHouseClusterConfig{
			{BaseDatasourceConfig: BaseDatasourceConfig{Name: "clickhouse-raw"}, Host: "example.com", Port: 8123, Username: "user", Password: "pass"},
		},
	}
	cfg.ApplyDefaults()

	srv, err := newServer(logrus.New(), cfg, "http://proxy.test", "18081")
	if err != nil {
		t.Fatalf("newServer failed: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/metadata", nil)
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	var got AuthMetadataResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	if got.Enabled {
		t.Fatal("expected enabled=false for none mode")
	}
}

func TestBrandingEndpointReturnsConfigWhenSet(t *testing.T) {
	t.Parallel()

	cfg := ServerConfig{
		Auth: AuthConfig{
			Mode: AuthModeNone,
			SuccessPage: &auth.SuccessPageConfig{
				Default: &auth.SuccessPageDisplay{
					Tagline: "Welcome to panda!",
				},
			},
		},
		ClickHouse: []ClickHouseClusterConfig{
			{BaseDatasourceConfig: BaseDatasourceConfig{Name: "clickhouse-raw"}, Host: "example.com", Port: 8123, Username: "user", Password: "pass"},
		},
	}
	cfg.ApplyDefaults()

	srv, err := newServer(logrus.New(), cfg, "http://proxy.test", "18081")
	if err != nil {
		t.Fatalf("newServer failed: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/branding", nil)
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	var got auth.SuccessPageConfig
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	if got.Default == nil || got.Default.Tagline != "Welcome to panda!" {
		t.Fatalf("unexpected default tagline: %+v", got.Default)
	}
}

func TestBrandingEndpointReturns204WhenNotConfigured(t *testing.T) {
	t.Parallel()

	cfg := ServerConfig{
		Auth: AuthConfig{Mode: AuthModeNone},
		ClickHouse: []ClickHouseClusterConfig{
			{BaseDatasourceConfig: BaseDatasourceConfig{Name: "clickhouse-raw"}, Host: "example.com", Port: 8123, Username: "user", Password: "pass"},
		},
	}
	cfg.ApplyDefaults()

	srv, err := newServer(logrus.New(), cfg, "http://proxy.test", "18081")
	if err != nil {
		t.Fatalf("newServer failed: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/branding", nil)
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d", http.StatusNoContent, rec.Code)
	}
}

func TestServerClickHouseQueryRejectsAuthModesRequiringIdentity(t *testing.T) {
	t.Parallel()

	for _, mode := range []AuthMode{AuthModeOAuth, AuthModeOIDC} {
		t.Run(string(mode), func(t *testing.T) {
			t.Parallel()

			srv := &server{cfg: ServerConfig{Auth: AuthConfig{Mode: mode}}}

			_, err := srv.ClickHouseQuery(context.Background(), "xatu", "SELECT 1", nil)
			if err == nil {
				t.Fatal("ClickHouseQuery error = nil, want auth mode guard")
			}
			if !strings.Contains(err.Error(), `requires proxy auth.mode="none"`) {
				t.Fatalf("ClickHouseQuery error = %q, want clear auth.mode=none configuration error", err)
			}
		})
	}
}

func TestServerClickHouseQueryRoutesInNoAuthMode(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("upstream method = %s, want POST", r.Method)
		}
		if got := r.URL.Query().Get("default_format"); got != "JSON" {
			t.Fatalf("upstream default_format = %q, want JSON", got)
		}
		if got := r.URL.Query().Get("database"); got != "default" {
			t.Fatalf("upstream database = %q, want default", got)
		}

		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(upstream.Close)

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream URL: %v", err)
	}

	host, portValue, err := net.SplitHostPort(upstreamURL.Host)
	if err != nil {
		t.Fatalf("split upstream host: %v", err)
	}

	port, err := strconv.Atoi(portValue)
	if err != nil {
		t.Fatalf("parse upstream port: %v", err)
	}

	cfg := ServerConfig{
		Auth: AuthConfig{Mode: AuthModeNone},
		ClickHouse: []ClickHouseClusterConfig{
			{
				BaseDatasourceConfig: BaseDatasourceConfig{Name: "xatu"},
				Host:                 host,
				Port:                 port,
				Database:             "default",
			},
		},
	}
	cfg.ApplyDefaults()

	srv, err := newServer(logrus.New(), cfg, "http://proxy.test", "18081")
	if err != nil {
		t.Fatalf("newServer failed: %v", err)
	}

	body, err := srv.ClickHouseQuery(
		context.Background(),
		"xatu",
		"SELECT 1",
		url.Values{"default_format": {"JSON"}},
	)
	if err != nil {
		t.Fatalf("ClickHouseQuery error = %v", err)
	}
	if string(body) != "ok" {
		t.Fatalf("ClickHouseQuery body = %q, want ok", body)
	}
}
