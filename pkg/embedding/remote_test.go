package embedding

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newMockProxy creates a test server that handles /embed and /embed/check.
// checkHandler can be nil if the test doesn't expect a check call.
func newMockProxy(
	t *testing.T,
	embedHandler http.HandlerFunc,
	checkHandler http.HandlerFunc,
) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/embed/check", func(w http.ResponseWriter, r *http.Request) {
		if checkHandler != nil {
			checkHandler(w, r)

			return
		}

		// Default: nothing cached.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(embedCheckResponse{})
	})
	mux.HandleFunc("/embed", embedHandler)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return srv
}

func TestRemoteEmbedder_Embed(t *testing.T) {
	t.Parallel()

	fakeVector := []float32{0.1, 0.2, 0.3}

	// Single embed goes directly to /embed, no /embed/check call.
	srv := newMockProxy(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)

		var req embedRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		require.Len(t, req.Items, 1)

		resp := embedResponse{
			Model:   "test-model",
			Results: []embedResult{{Hash: req.Items[0].Hash, Vector: fakeVector}},
		}

		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}, nil)

	embedder := NewRemote(logrus.New(), srv.URL, func() string { return "" })

	vec, err := embedder.Embed("hello world")
	require.NoError(t, err)
	assert.Equal(t, fakeVector, vec)
}

func TestRemoteEmbedder_EmbedBatch_AllMisses(t *testing.T) {
	t.Parallel()

	texts := []string{"alpha", "beta", "gamma"}
	fakeVectors := map[string][]float32{
		"alpha": {1.0, 0.0, 0.0},
		"beta":  {0.0, 1.0, 0.0},
		"gamma": {0.0, 0.0, 1.0},
	}

	var checkCalled atomic.Bool

	srv := newMockProxy(t,
		func(w http.ResponseWriter, r *http.Request) {
			var req embedRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))

			results := make([]embedResult, 0, len(req.Items))
			for _, item := range req.Items {
				results = append(results, embedResult{
					Hash:   item.Hash,
					Vector: fakeVectors[item.Text],
				})
			}

			w.Header().Set("Content-Type", "application/json")
			require.NoError(t, json.NewEncoder(w).Encode(embedResponse{
				Model:   "test-model",
				Results: results,
			}))
		},
		func(w http.ResponseWriter, r *http.Request) {
			checkCalled.Store(true)
			// Nothing cached.
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(embedCheckResponse{})
		},
	)

	embedder := NewRemote(logrus.New(), srv.URL, func() string { return "" })

	vectors, err := embedder.EmbedBatch(texts)
	require.NoError(t, err)
	require.Len(t, vectors, 3)

	assert.True(t, checkCalled.Load(), "/embed/check should be called for batch > 1")
	assert.Equal(t, fakeVectors["alpha"], vectors[0])
	assert.Equal(t, fakeVectors["beta"], vectors[1])
	assert.Equal(t, fakeVectors["gamma"], vectors[2])
}

func TestRemoteEmbedder_EmbedBatch_AllCached(t *testing.T) {
	t.Parallel()

	texts := []string{"alpha", "beta"}
	cachedVectors := map[string][]float32{
		sha256Hex("alpha"): {1.0, 0.0},
		sha256Hex("beta"):  {0.0, 1.0},
	}

	var embedCalled atomic.Bool

	srv := newMockProxy(t,
		func(w http.ResponseWriter, _ *http.Request) {
			embedCalled.Store(true)
			http.Error(w, "should not be called", http.StatusInternalServerError)
		},
		func(w http.ResponseWriter, r *http.Request) {
			var req embedCheckRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))

			results := make([]embedResult, 0, len(req.Hashes))
			for _, h := range req.Hashes {
				if vec, ok := cachedVectors[h]; ok {
					results = append(results, embedResult{Hash: h, Vector: vec})
				}
			}

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(embedCheckResponse{Cached: results})
		},
	)

	embedder := NewRemote(logrus.New(), srv.URL, func() string { return "" })

	vectors, err := embedder.EmbedBatch(texts)
	require.NoError(t, err)
	require.Len(t, vectors, 2)

	assert.False(t, embedCalled.Load(), "/embed should NOT be called when everything is cached")
	assert.Equal(t, cachedVectors[sha256Hex("alpha")], vectors[0])
	assert.Equal(t, cachedVectors[sha256Hex("beta")], vectors[1])
}

func TestRemoteEmbedder_EmbedBatch_PartialCache(t *testing.T) {
	t.Parallel()

	texts := []string{"cached-text", "uncached-text"}
	cachedHash := sha256Hex("cached-text")
	cachedVec := []float32{1.0, 0.0}
	uncachedVec := []float32{0.0, 1.0}

	srv := newMockProxy(t,
		func(w http.ResponseWriter, r *http.Request) {
			var req embedRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			// Only the uncached item should be sent.
			require.Len(t, req.Items, 1)
			assert.Equal(t, "uncached-text", req.Items[0].Text)

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(embedResponse{
				Model:   "test-model",
				Results: []embedResult{{Hash: req.Items[0].Hash, Vector: uncachedVec}},
			})
		},
		func(w http.ResponseWriter, _ *http.Request) {
			// Only "cached-text" is cached.
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(embedCheckResponse{
				Cached: []embedResult{{Hash: cachedHash, Vector: cachedVec}},
			})
		},
	)

	embedder := NewRemote(logrus.New(), srv.URL, func() string { return "" })

	vectors, err := embedder.EmbedBatch(texts)
	require.NoError(t, err)
	require.Len(t, vectors, 2)

	assert.Equal(t, cachedVec, vectors[0])
	assert.Equal(t, uncachedVec, vectors[1])
}

func TestRemoteEmbedder_EmbedBatch_DuplicateTexts(t *testing.T) {
	t.Parallel()

	texts := []string{"duplicate", "duplicate"}
	fakeVector := []float32{0.5, 0.5, 0.5}

	srv := newMockProxy(t,
		func(w http.ResponseWriter, r *http.Request) {
			var req embedRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))

			hash := sha256Hex("duplicate")
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(embedResponse{
				Model:   "test-model",
				Results: []embedResult{{Hash: hash, Vector: fakeVector}},
			})
		},
		nil,
	)

	embedder := NewRemote(logrus.New(), srv.URL, func() string { return "" })

	vectors, err := embedder.EmbedBatch(texts)
	require.NoError(t, err)
	require.Len(t, vectors, 2)

	assert.Equal(t, fakeVector, vectors[0])
	assert.Equal(t, fakeVector, vectors[1])
}

func TestRemoteEmbedder_ServerError(t *testing.T) {
	t.Parallel()

	srv := newMockProxy(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}, nil)

	embedder := NewRemote(logrus.New(), srv.URL, func() string { return "" })

	_, err := embedder.Embed("test")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")
}

func TestRemoteEmbedder_AuthHeader(t *testing.T) {
	t.Parallel()

	const expectedToken = "my-secret-token"
	var tokenCalled atomic.Bool

	srv := newMockProxy(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer "+expectedToken, r.Header.Get("Authorization"))

		hash := fmt.Sprintf("%x", sha256.Sum256([]byte("test")))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(embedResponse{
			Model:   "test-model",
			Results: []embedResult{{Hash: hash, Vector: []float32{0.1, 0.2, 0.3}}},
		})
	}, nil)

	embedder := NewRemote(logrus.New(), srv.URL, func() string {
		tokenCalled.Store(true)

		return expectedToken
	})

	_, err := embedder.Embed("test")
	require.NoError(t, err)
	assert.True(t, tokenCalled.Load(), "token function should have been called")
}
