package embedding

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProbeNegotiatesV3WhenAvailable(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/v3/embedding/check", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)

		var req struct {
			Hashes []string `json:"hashes"`
			Task   string   `json:"task"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Empty(t, req.Hashes)
		assert.Equal(t, taskQuery, req.Task)

		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(embedCheckResponse{
			Model:      "google/gemini-embedding-2",
			Dimensions: 768,
		}))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	model, dims, protocol := Probe(context.Background(), srv.URL, nil, "legacy-model")

	assert.Equal(t, "google/gemini-embedding-2", model)
	assert.Equal(t, 768, dims)
	assert.Equal(t, ProtocolV3, protocol)
}

func TestProbeV3RejectsNonPositiveDimensions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		dims int
	}{
		{name: "zero", dims: 0},
		{name: "negative", dims: -1},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mux := http.NewServeMux()
			mux.HandleFunc("/v3/embedding/check", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				require.NoError(t, json.NewEncoder(w).Encode(embedCheckResponse{
					Model:      "google/gemini-embedding-2",
					Dimensions: tt.dims,
				}))
			})

			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)

			model, dims, ok := ProbeV3(context.Background(), srv.URL, nil)

			assert.False(t, ok)
			assert.Empty(t, model)
			assert.Zero(t, dims)
		})
	}
}

func TestProbeNegotiatesV2WhenOnlyV2Available(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/v2/embedding/check", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)

		var req struct {
			Hashes []string `json:"hashes"`
			Task   *string  `json:"task"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Empty(t, req.Hashes)
		assert.Nil(t, req.Task)

		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(embedCheckResponse{
			Model:      "google/gemini-embedding-2",
			Dimensions: 1536,
		}))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	model, dims, protocol := Probe(context.Background(), srv.URL, nil, "legacy-model")

	assert.Equal(t, "google/gemini-embedding-2", model)
	assert.Equal(t, 1536, dims)
	assert.Equal(t, ProtocolV2, protocol)
}

func TestProbeNegotiatesHistoricalV2WithoutDimensions(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/v2/embedding/check", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(embedCheckResponse{
			Model: "google/gemini-embedding-2",
		}))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	model, dims, protocol := Probe(context.Background(), srv.URL, nil, "legacy-model")

	assert.Equal(t, "google/gemini-embedding-2", model)
	assert.Equal(t, defaultV2Dimensions, dims)
	assert.Equal(t, ProtocolV2, protocol)
}

func TestProbeNegotiatesV1OnlyWhenLegacyCheckExists(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/embed/check", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)

		var req embedCheckRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Empty(t, req.Hashes)

		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"cached": []embedResult{}}))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	model, dims, protocol := Probe(context.Background(), srv.URL, nil, "legacy-model")

	assert.Equal(t, "legacy-model", model)
	assert.Zero(t, dims)
	assert.Equal(t, ProtocolV1, protocol)
}

func TestProbeReturnsUnknownWhenFallbackModelHasNoLegacyEndpoint(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)

	model, dims, protocol := Probe(context.Background(), srv.URL, nil, "legacy-model")

	assert.Empty(t, model)
	assert.Zero(t, dims)
	assert.Equal(t, ProtocolUnknown, protocol)
}

func TestProbeReturnsUnknownWithoutFallbackModel(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)

	model, dims, protocol := Probe(context.Background(), srv.URL, nil, "")

	assert.Empty(t, model)
	assert.Zero(t, dims)
	assert.Equal(t, ProtocolUnknown, protocol)
}
