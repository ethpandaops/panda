package proxy

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/cache"
)

// newRecordingOpenRouterServer mimics the OpenRouter /v1/embeddings endpoint and
// records the dimensions field from the most recent request, so tests can assert
// what the embedding service sent upstream.
func newRecordingOpenRouterServer(t *testing.T, lastDims *int, mu *sync.Mutex) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/embeddings", r.URL.Path)

		var req openRouterRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))

		mu.Lock()
		*lastDims = req.Dimensions
		mu.Unlock()

		data := make([]openRouterEmbedding, 0, len(req.Input))
		for i := range req.Input {
			vec := make([]float32, 1536)
			vec[0], vec[1], vec[2] = 0.3, 0.4, 0.5
			data = append(data, openRouterEmbedding{Index: i, Embedding: vec})
		}

		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(openRouterResponse{Data: data}))
	}))
	t.Cleanup(srv.Close)

	return srv
}

func TestEmbeddingServiceV2_SendsDimensions(t *testing.T) {
	t.Parallel()

	var (
		lastDims int
		mu       sync.Mutex
	)

	mockAPI := newRecordingOpenRouterServer(t, &lastDims, &mu)

	svc := NewEmbeddingServiceWithDimensions(
		logrus.New(), cache.NewInMemory(0),
		"test-api-key", "google/gemini-embedding-2", mockAPI.URL+"/v1", 0.01, 1536,
	)

	assert.Equal(t, "google/gemini-embedding-2", svc.Model())
	assert.Equal(t, 1536, svc.Dimensions())

	resp, err := svc.Embed(context.Background(), []EmbedItem{{Hash: "aaa", Text: "hello"}}, "")
	require.NoError(t, err)
	require.Len(t, resp.Results, 1)
	assert.Equal(t, "google/gemini-embedding-2", resp.Model)

	mu.Lock()
	assert.Equal(t, 1536, lastDims, "v2 service must request dimensions=1536 upstream")
	mu.Unlock()

	// Vector is L2-normalized.
	var norm float64
	for _, v := range resp.Results[0].Vector {
		norm += float64(v) * float64(v)
	}

	assert.InDelta(t, 1.0, math.Sqrt(norm), 1e-6)
}

func TestEmbeddingServiceV2_CheckCachedDimensions(t *testing.T) {
	t.Parallel()

	var (
		lastDims int
		mu       sync.Mutex
	)

	mockAPI := newRecordingOpenRouterServer(t, &lastDims, &mu)
	memCache := cache.NewInMemory(0)

	svc := NewEmbeddingServiceWithDimensions(
		logrus.New(), memCache,
		"test-api-key", "google/gemini-embedding-2", mockAPI.URL+"/v1", 0.01, 1536,
	)

	// Nothing cached yet.
	results, err := svc.CheckCached(context.Background(), []string{"aaa"}, "")
	require.NoError(t, err)
	assert.Empty(t, results)

	// Embed populates the cache, then the same hash is a hit.
	_, err = svc.Embed(context.Background(), []EmbedItem{{Hash: "aaa", Text: "hello"}}, "")
	require.NoError(t, err)

	results, err = svc.CheckCached(context.Background(), []string{"aaa"}, "")
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "aaa", results[0].Hash)

	cached, err := memCache.GetMulti(context.Background(), []string{"google/gemini-embedding-2:1536:aaa"})
	require.NoError(t, err)
	assert.Len(t, cached, 1)
}

func TestEmbeddingServiceV1_OmitsDimensions(t *testing.T) {
	t.Parallel()

	var (
		lastDims = -1
		mu       sync.Mutex
	)

	mockAPI := newRecordingOpenRouterServer(t, &lastDims, &mu)
	memCache := cache.NewInMemory(0)

	svc := NewEmbeddingService(
		logrus.New(), memCache,
		"test-api-key", "openai/text-embedding-3-small", mockAPI.URL+"/v1", 0.01,
	)

	assert.Equal(t, 0, svc.Dimensions())

	_, err := svc.Embed(context.Background(), []EmbedItem{{Hash: "aaa", Text: "hello"}}, "")
	require.NoError(t, err)

	mu.Lock()
	assert.Equal(t, 0, lastDims, "v1 request must omit dimensions")
	mu.Unlock()

	cached, err := memCache.GetMulti(context.Background(), []string{"openai/text-embedding-3-small:aaa"})
	require.NoError(t, err)
	assert.Len(t, cached, 1)
}
