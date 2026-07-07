package embedding

import (
	"context"
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

	"github.com/ethpandaops/panda/pkg/cache"
)

// newMockProxy creates a test server that handles /v3/embedding and /v3/embedding/check.
// checkHandler can be nil if the test doesn't expect a check call.
func newMockProxy(
	t *testing.T,
	embedHandler http.HandlerFunc,
	checkHandler http.HandlerFunc,
) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/v3/embedding/check", func(w http.ResponseWriter, r *http.Request) {
		if checkHandler != nil {
			checkHandler(w, r)

			return
		}

		// Default: nothing cached.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(embedCheckResponse{Dimensions: 8})
	})
	mux.HandleFunc("/v3/embedding", embedHandler)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return srv
}

func TestRemoteEmbedder_Embed(t *testing.T) {
	t.Parallel()

	fakeVector := []float32{0.1, 0.2, 0.3}

	// Single embed goes directly to /v3/embedding, no /v3/embedding/check call.
	srv := newMockProxy(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)

		var req embedRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		require.Len(t, req.Items, 1)

		resp := embedResponse{Dimensions: 8,
			Model:   "",
			Results: []embedResult{{Hash: req.Items[0].Hash, Vector: fakeVector}},
		}

		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}, nil)

	embedder := NewRemote(logrus.New(), srv.URL, func() string { return "" }, nil, nil, "", 8, ProtocolV3)

	vec, err := embedder.Embed("hello world")
	require.NoError(t, err)
	assert.Equal(t, fakeVector, vec)
}

func TestRemoteEmbedder_ProtocolRouting(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		protocol Protocol
		path     string
		model    string
		dims     int
		wantTask *string
	}{
		{
			name:     "v3",
			protocol: ProtocolV3,
			path:     "/v3/embedding",
			model:    "model-v3",
			dims:     8,
			wantTask: stringPtr(taskQuery),
		},
		{
			name:     "v2",
			protocol: ProtocolV2,
			path:     "/v2/embedding",
			model:    "model-v2",
			dims:     8,
		},
		{
			name:     "v1",
			protocol: ProtocolV1,
			path:     "/embed",
			model:    "model-v1",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mux := http.NewServeMux()
			mux.HandleFunc(tt.path, func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, http.MethodPost, r.Method)

				var req struct {
					Items []embedItem `json:"items"`
					Task  *string     `json:"task"`
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
				require.Len(t, req.Items, 1)

				if tt.wantTask == nil {
					assert.Nil(t, req.Task, "task must be omitted for symmetric protocols")
				} else if assert.NotNil(t, req.Task) {
					assert.Equal(t, *tt.wantTask, *req.Task)
				}

				resp := map[string]any{
					"model":   tt.model,
					"results": []embedResult{{Hash: req.Items[0].Hash, Vector: []float32{1, 0, 0}}},
				}
				if tt.dims > 0 {
					resp["dimensions"] = tt.dims
				}

				w.Header().Set("Content-Type", "application/json")
				require.NoError(t, json.NewEncoder(w).Encode(resp))
			})

			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)

			embedder := NewRemote(logrus.New(), srv.URL, func() string { return "" }, nil, nil, tt.model, tt.dims, tt.protocol)

			vec, err := embedder.Embed("hello")
			require.NoError(t, err)
			assert.Equal(t, []float32{1, 0, 0}, vec)
		})
	}
}

func stringPtr(s string) *string {
	return &s
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
			require.NoError(t, json.NewEncoder(w).Encode(embedResponse{Dimensions: 8,
				Model:   "",
				Results: results,
			}))
		},
		func(w http.ResponseWriter, r *http.Request) {
			checkCalled.Store(true)
			// Nothing cached.
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(embedCheckResponse{Dimensions: 8})
		},
	)

	embedder := NewRemote(logrus.New(), srv.URL, func() string { return "" }, nil, nil, "", 8, ProtocolV3)

	vectors, err := embedder.EmbedBatch(texts)
	require.NoError(t, err)
	require.Len(t, vectors, 3)

	assert.True(t, checkCalled.Load(), "/v3/embedding/check should be called for batch > 1")
	assert.Equal(t, fakeVectors["alpha"], vectors[0])
	assert.Equal(t, fakeVectors["beta"], vectors[1])
	assert.Equal(t, fakeVectors["gamma"], vectors[2])
}

func TestRemoteEmbedder_EmbedBatch_ReportsProgress(t *testing.T) {
	t.Parallel()

	texts := []string{"alpha", "beta", "gamma"}

	srv := newMockProxy(t,
		func(w http.ResponseWriter, r *http.Request) {
			var req embedRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))

			results := make([]embedResult, 0, len(req.Items))
			for _, item := range req.Items {
				results = append(results, embedResult{Hash: item.Hash, Vector: []float32{1, 0, 0}})
			}

			w.Header().Set("Content-Type", "application/json")
			require.NoError(t, json.NewEncoder(w).Encode(embedResponse{
				Dimensions: 8, Results: results}))
		},
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(embedCheckResponse{Dimensions: 8})
		},
	)

	embedder := NewRemote(logrus.New(), srv.URL, func() string { return "" }, nil, nil, "", 8, ProtocolV3)

	var calls, lastDone, lastTotal int
	embedder.OnProgress(func(completed, total int) {
		calls++
		lastDone, lastTotal = completed, total
	})

	_, err := embedder.EmbedBatch(texts)
	require.NoError(t, err)

	assert.Positive(t, calls, "OnProgress should be invoked during a batch embed")
	assert.Equal(t, 3, lastDone, "final progress should report every document done")
	assert.Equal(t, 3, lastTotal)
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
			_ = json.NewEncoder(w).Encode(embedCheckResponse{
				Dimensions: 8, Cached: results})
		},
	)

	embedder := NewRemote(logrus.New(), srv.URL, func() string { return "" }, nil, nil, "", 8, ProtocolV3)

	vectors, err := embedder.EmbedBatch(texts)
	require.NoError(t, err)
	require.Len(t, vectors, 2)

	assert.False(t, embedCalled.Load(), "/v3/embedding should NOT be called when everything is cached")
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
			_ = json.NewEncoder(w).Encode(embedResponse{Dimensions: 8,
				Model:   "",
				Results: []embedResult{{Hash: req.Items[0].Hash, Vector: uncachedVec}},
			})
		},
		func(w http.ResponseWriter, _ *http.Request) {
			// Only "cached-text" is cached.
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(embedCheckResponse{Dimensions: 8,
				Cached: []embedResult{{Hash: cachedHash, Vector: cachedVec}},
			})
		},
	)

	embedder := NewRemote(logrus.New(), srv.URL, func() string { return "" }, nil, nil, "", 8, ProtocolV3)

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
			_ = json.NewEncoder(w).Encode(embedResponse{Dimensions: 8,
				Model:   "",
				Results: []embedResult{{Hash: hash, Vector: fakeVector}},
			})
		},
		nil,
	)

	embedder := NewRemote(logrus.New(), srv.URL, func() string { return "" }, nil, nil, "", 8, ProtocolV3)

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

	embedder := NewRemote(logrus.New(), srv.URL, func() string { return "" }, nil, nil, "", 8, ProtocolV3)

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
		_ = json.NewEncoder(w).Encode(embedResponse{Dimensions: 8,
			Model:   "",
			Results: []embedResult{{Hash: hash, Vector: []float32{0.1, 0.2, 0.3}}},
		})
	}, nil)

	embedder := NewRemote(logrus.New(), srv.URL, func() string {
		tokenCalled.Store(true)

		return expectedToken
	}, nil, nil, "", 8, ProtocolV3)

	_, err := embedder.Embed("test")
	require.NoError(t, err)
	assert.True(t, tokenCalled.Load(), "token function should have been called")
}

func TestRemoteEmbedder_V2ToleratesMissingDimensionsEchoAndUsesProxyCache(t *testing.T) {
	t.Parallel()

	cachedVectors := map[string][]float32{
		sha256Hex("alpha"): {1, 0},
		sha256Hex("beta"):  {0, 1},
	}

	var embedCalled atomic.Bool

	mux := http.NewServeMux()
	mux.HandleFunc("/v2/embedding/check", func(w http.ResponseWriter, r *http.Request) {
		var req embedCheckRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))

		results := make([]embedResult, 0, len(req.Hashes))
		for _, h := range req.Hashes {
			if vec, ok := cachedVectors[h]; ok {
				results = append(results, embedResult{Hash: h, Vector: vec})
			}
		}

		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(embedCheckResponse{
			Model:  "model-v2",
			Cached: results,
		}))
	})
	mux.HandleFunc("/v2/embedding", func(w http.ResponseWriter, _ *http.Request) {
		embedCalled.Store(true)
		http.Error(w, "should not embed cached vectors", http.StatusInternalServerError)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	embedder := NewRemote(logrus.New(), srv.URL, func() string { return "" }, nil, nil, "model-v2", defaultV2Dimensions, ProtocolV2)

	vectors, err := embedder.EmbedBatch([]string{"alpha", "beta"})
	require.NoError(t, err)
	require.Len(t, vectors, 2)
	assert.False(t, embedCalled.Load())
	assert.Equal(t, cachedVectors[sha256Hex("alpha")], vectors[0])
	assert.Equal(t, cachedVectors[sha256Hex("beta")], vectors[1])
}

func TestRemoteEmbedder_V2RejectsNonZeroDimensionsMismatch(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/v2/embedding/check", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(embedCheckResponse{
			Model:      "model-v2",
			Dimensions: 768,
			Cached: []embedResult{{
				Hash:   sha256Hex("alpha"),
				Vector: []float32{1, 0},
			}},
		}))
	})
	mux.HandleFunc("/v2/embedding", func(w http.ResponseWriter, r *http.Request) {
		var req embedRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))

		results := make([]embedResult, 0, len(req.Items))
		for _, item := range req.Items {
			results = append(results, embedResult{Hash: item.Hash, Vector: []float32{1, 0}})
		}

		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(embedResponse{
			Model:      "model-v2",
			Dimensions: 768,
			Results:    results,
		}))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	embedder := NewRemote(logrus.New(), srv.URL, func() string { return "" }, nil, nil, "model-v2", defaultV2Dimensions, ProtocolV2)

	_, err := embedder.EmbedBatch([]string{"alpha", "beta"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "embedding space")
}

func TestRemoteEmbedder_LocalCacheKeyShapesByProtocol(t *testing.T) {
	t.Parallel()

	const text = "same text"

	tests := []struct {
		name     string
		protocol Protocol
		path     string
		model    string
		dims     int
		wantKey  string
	}{
		{
			name:     "v1",
			protocol: ProtocolV1,
			path:     "/embed",
			model:    "model-v1",
			wantKey:  "model-v1:" + sha256Hex(text),
		},
		{
			name:     "v2",
			protocol: ProtocolV2,
			path:     "/v2/embedding",
			model:    "model-v2",
			dims:     8,
			wantKey:  "model-v2:8:" + sha256Hex(text),
		},
		{
			name:     "v3",
			protocol: ProtocolV3,
			path:     "/v3/embedding",
			model:    "model-v3",
			dims:     8,
			wantKey:  "model-v3:8:query:" + sha256Hex(text),
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mux := http.NewServeMux()
			mux.HandleFunc(tt.path, func(w http.ResponseWriter, r *http.Request) {
				var req embedRequest
				require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
				require.Len(t, req.Items, 1)

				w.Header().Set("Content-Type", "application/json")
				require.NoError(t, json.NewEncoder(w).Encode(embedResponse{
					Model:      tt.model,
					Dimensions: tt.dims,
					Results: []embedResult{{
						Hash:   req.Items[0].Hash,
						Vector: []float32{1, 0},
					}},
				}))
			})

			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)

			localCache := cache.NewInMemory(0)
			embedder := NewRemote(logrus.New(), srv.URL, func() string { return "" }, nil, localCache, tt.model, tt.dims, tt.protocol)

			_, err := embedder.Embed(text)
			require.NoError(t, err)

			got, err := localCache.GetMulti(context.Background(), []string{tt.wantKey})
			require.NoError(t, err)
			assert.Len(t, got, 1)
		})
	}
}

func TestRemoteEmbedder_V3RejectsWrongModelEcho(t *testing.T) {
	t.Parallel()

	srv := newMockProxy(t, func(w http.ResponseWriter, r *http.Request) {
		var req embedRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))

		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(embedResponse{
			Model:      "wrong-model",
			Dimensions: 8,
			Results: []embedResult{{
				Hash:   req.Items[0].Hash,
				Vector: []float32{1, 0},
			}},
		}))
	}, nil)

	embedder := NewRemote(logrus.New(), srv.URL, func() string { return "" }, nil, nil, "expected-model", 8, ProtocolV3)

	_, err := embedder.Embed("hello")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "embedding space")
}

func TestRemoteEmbedder_V1IgnoresSpaceEcho(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/embed", func(w http.ResponseWriter, r *http.Request) {
		var req embedRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))

		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(embedResponse{
			Model:      "different-model",
			Dimensions: 768,
			Results: []embedResult{{
				Hash:   req.Items[0].Hash,
				Vector: []float32{1, 0},
			}},
		}))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	embedder := NewRemote(logrus.New(), srv.URL, func() string { return "" }, nil, nil, "expected-model", 1536, ProtocolV1)

	vec, err := embedder.Embed("hello")
	require.NoError(t, err)
	assert.Equal(t, []float32{1, 0}, vec)
}
