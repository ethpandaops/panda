package proxy

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/cache"
)

const testModel = "test-embed-model"

// newMockOpenRouterServer creates a test server that mimics the OpenRouter /v1/embeddings endpoint.
// The handler generates fake embeddings for each input text.
// apiCalls is incremented on each request so tests can verify whether the API was called.
func newMockOpenRouterServer(t *testing.T, apiCalls *atomic.Int32) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiCalls.Add(1)

		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/v1/embeddings", r.URL.Path)
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))
		require.Equal(t, "Bearer test-api-key", r.Header.Get("Authorization"))

		var req openRouterRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))

		data := make([]openRouterEmbedding, 0, len(req.Input))
		for i := range req.Input {
			data = append(data, openRouterEmbedding{
				Index:     i,
				Embedding: []float32{0.3, 0.4, 0.5},
			})
		}

		resp := openRouterResponse{Data: data}

		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	t.Cleanup(srv.Close)

	return srv
}

func TestEmbeddingService_Embed_CacheMiss(t *testing.T) {
	t.Parallel()

	var apiCalls atomic.Int32

	mockAPI := newMockOpenRouterServer(t, &apiCalls)

	memCache := cache.NewInMemory(0)
	svc := NewEmbeddingServiceWithDimensions(logrus.New(), memCache, "test-api-key", testModel, mockAPI.URL+"/v1", 0.01, 3)

	items := []EmbedItem{
		{Hash: "aaa", Text: "hello"},
		{Hash: "bbb", Text: "world"},
	}

	resp, err := svc.Embed(context.Background(), items, EmbedTaskDocument)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, testModel, resp.Model)
	require.Len(t, resp.Results, 2)

	// Verify both results have vectors.
	assert.Equal(t, "aaa", resp.Results[0].Hash)
	assert.NotEmpty(t, resp.Results[0].Vector)
	assert.Equal(t, "bbb", resp.Results[1].Hash)
	assert.NotEmpty(t, resp.Results[1].Vector)

	// API should have been called exactly once.
	assert.Equal(t, int32(1), apiCalls.Load())

	// Verify results are cached for subsequent requests.
	cached, err := memCache.GetMulti(context.Background(), []string{
		testModel + ":3:document:aaa",
		testModel + ":3:document:bbb",
	})
	require.NoError(t, err)
	assert.Len(t, cached, 2)
}

func TestEmbeddingService_Embed_CacheHit(t *testing.T) {
	t.Parallel()

	var apiCalls atomic.Int32

	mockAPI := newMockOpenRouterServer(t, &apiCalls)

	memCache := cache.NewInMemory(0)

	// Pre-populate the cache with vectors.
	cachedVec := []float32{0.6, 0.7, 0.1}
	vecData, err := json.Marshal(cachedVec)
	require.NoError(t, err)

	require.NoError(t, memCache.SetMulti(context.Background(), map[string][]byte{
		testModel + ":3:document:aaa": vecData,
		testModel + ":3:document:bbb": vecData,
	}))

	svc := NewEmbeddingServiceWithDimensions(logrus.New(), memCache, "test-api-key", testModel, mockAPI.URL+"/v1", 0.01, 3)

	items := []EmbedItem{
		{Hash: "aaa", Text: "hello"},
		{Hash: "bbb", Text: "world"},
	}

	resp, err := svc.Embed(context.Background(), items, EmbedTaskDocument)
	require.NoError(t, err)
	require.Len(t, resp.Results, 2)

	// Both should come from cache.
	assert.Equal(t, cachedVec, resp.Results[0].Vector)
	assert.Equal(t, cachedVec, resp.Results[1].Vector)

	// API should NOT have been called.
	assert.Equal(t, int32(0), apiCalls.Load())
}

func TestEmbeddingService_Embed_PartialCacheHit(t *testing.T) {
	t.Parallel()

	var apiCalls atomic.Int32

	mockAPI := newMockOpenRouterServer(t, &apiCalls)

	memCache := cache.NewInMemory(0)

	// Pre-populate only the first item in the cache.
	cachedVec := []float32{0.9, 0.1, 0.0}
	vecData, err := json.Marshal(cachedVec)
	require.NoError(t, err)

	require.NoError(t, memCache.Set(context.Background(), testModel+":3:document:aaa", vecData))

	svc := NewEmbeddingServiceWithDimensions(logrus.New(), memCache, "test-api-key", testModel, mockAPI.URL+"/v1", 0.01, 3)

	items := []EmbedItem{
		{Hash: "aaa", Text: "hello"},
		{Hash: "bbb", Text: "world"},
		{Hash: "ccc", Text: "foo"},
	}

	resp, err := svc.Embed(context.Background(), items, EmbedTaskDocument)
	require.NoError(t, err)
	require.Len(t, resp.Results, 3)

	// First item should come from cache.
	assert.Equal(t, cachedVec, resp.Results[0].Vector)
	assert.Equal(t, "aaa", resp.Results[0].Hash)

	// Second and third items should come from API (l2-normalized).
	assert.NotEmpty(t, resp.Results[1].Vector)
	assert.NotEmpty(t, resp.Results[2].Vector)

	// API should have been called exactly once (for the 2 misses).
	assert.Equal(t, int32(1), apiCalls.Load())
}

func TestEmbeddingService_Embed_Empty(t *testing.T) {
	t.Parallel()

	var apiCalls atomic.Int32

	mockAPI := newMockOpenRouterServer(t, &apiCalls)

	memCache := cache.NewInMemory(0)
	svc := NewEmbeddingServiceWithDimensions(logrus.New(), memCache, "test-api-key", testModel, mockAPI.URL+"/v1", 0.01, 3)

	resp, err := svc.Embed(context.Background(), []EmbedItem{}, EmbedTaskDocument)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, testModel, resp.Model)
	assert.Empty(t, resp.Results)

	// API should NOT have been called.
	assert.Equal(t, int32(0), apiCalls.Load())
}

func TestEmbeddingService_Embed_L2Normalized(t *testing.T) {
	t.Parallel()

	var apiCalls atomic.Int32

	mockAPI := newMockOpenRouterServer(t, &apiCalls)

	memCache := cache.NewInMemory(0)
	svc := NewEmbeddingServiceWithDimensions(logrus.New(), memCache, "test-api-key", testModel, mockAPI.URL+"/v1", 0.01, 3)

	items := []EmbedItem{
		{Hash: "aaa", Text: "test normalization"},
	}

	resp, err := svc.Embed(context.Background(), items, EmbedTaskDocument)
	require.NoError(t, err)
	require.Len(t, resp.Results, 1)

	vec := resp.Results[0].Vector
	require.NotEmpty(t, vec)

	// Compute L2 norm and verify it is approximately 1.0.
	var norm float64
	for _, v := range vec {
		norm += float64(v) * float64(v)
	}

	norm = math.Sqrt(norm)
	assert.InDelta(t, 1.0, norm, 1e-5, "returned vector should be L2-normalized")
}

func TestEmbeddingService_Embed_APIError(t *testing.T) {
	t.Parallel()

	errorServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
	}))
	t.Cleanup(errorServer.Close)

	memCache := cache.NewInMemory(0)
	svc := NewEmbeddingServiceWithDimensions(logrus.New(), memCache, "test-api-key", testModel, errorServer.URL+"/v1", 0, 3)

	items := []EmbedItem{
		{Hash: "aaa", Text: "hello"},
	}

	_, err := svc.Embed(context.Background(), items, EmbedTaskDocument)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "429")
}

// newTaskCapturingServer mimics the upstream embeddings endpoint and records
// the input_type of each request.
func newTaskCapturingServer(t *testing.T, inputTypes *[]string) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req openRouterRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))

		*inputTypes = append(*inputTypes, req.InputType)

		data := make([]openRouterEmbedding, 0, len(req.Input))
		for i, text := range req.Input {
			// Vary the vector by input_type so task-space mixing is detectable.
			base := float32(len(req.InputType) + len(text))
			data = append(data, openRouterEmbedding{Index: i, Embedding: []float32{base, 1, 2}})
		}

		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(openRouterResponse{Data: data}))
	}))
	t.Cleanup(srv.Close)

	return srv
}

func TestEmbeddingService_TaskScopesUpstreamAndCache(t *testing.T) {
	t.Parallel()

	var inputTypes []string

	mockAPI := newTaskCapturingServer(t, &inputTypes)
	memCache := cache.NewInMemory(0)
	svc := NewEmbeddingServiceWithDimensions(logrus.New(), memCache, "test-api-key", testModel, mockAPI.URL, 0.01, 3)

	items := []EmbedItem{{Hash: "aaa", Text: "hello"}}

	// Same text under query and document tasks must hit the upstream twice
	// (task-scoped cache keys never collide) with the right input_type each time.
	queryResp, err := svc.Embed(context.Background(), items, EmbedTaskQuery)
	require.NoError(t, err)

	docResp, err := svc.Embed(context.Background(), items, EmbedTaskDocument)
	require.NoError(t, err)

	assert.Equal(t, []string{"search_query", "search_document"}, inputTypes)
	assert.NotEqual(t, queryResp.Results[0].Vector, docResp.Results[0].Vector,
		"query and document vector spaces must not collide in cache")

	// Cached per task: re-embedding under each task adds no upstream calls.
	_, err = svc.Embed(context.Background(), items, EmbedTaskQuery)
	require.NoError(t, err)
	_, err = svc.Embed(context.Background(), items, EmbedTaskDocument)
	require.NoError(t, err)
	assert.Len(t, inputTypes, 2)

	// CheckCached is scoped the same way.
	cached, err := svc.CheckCached(context.Background(), []string{"aaa"}, EmbedTaskQuery)
	require.NoError(t, err)
	assert.Len(t, cached, 1)
	assert.Equal(t, queryResp.Results[0].Vector, cached[0].Vector)

	// Empty task is the symmetric v1/v2 path: no upstream input_type and no
	// task segment in the cache key.
	_, err = svc.Embed(context.Background(), items, "")
	require.NoError(t, err)
	assert.Equal(t, []string{"search_query", "search_document", ""}, inputTypes)

	cached, err = svc.CheckCached(context.Background(), []string{"aaa"}, "")
	require.NoError(t, err)
	assert.Len(t, cached, 1)

	// Unknown tasks are rejected.
	_, err = svc.Embed(context.Background(), items, "banana")
	require.Error(t, err)
	_, err = svc.CheckCached(context.Background(), []string{"aaa"}, "banana")
	require.Error(t, err)
}
