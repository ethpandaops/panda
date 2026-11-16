package searchruntime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/embedding"
	"github.com/ethpandaops/panda/pkg/module"
	"github.com/ethpandaops/panda/pkg/resource"
	"github.com/ethpandaops/panda/pkg/types"
	"github.com/ethpandaops/panda/runbooks"
)

// fakeEmbeddingBackend serves the proxy's v2 embedding routes well enough for
// embedding.RemoteEmbedder to talk to it, with a switch to make every real
// embed call fail on demand — letting a test control exactly which of
// reindex's index builds succeeds.
type fakeEmbeddingBackend struct {
	mu    sync.Mutex
	model string
	dims  int
	fail  bool
}

func (b *fakeEmbeddingBackend) configure(model string, dims int, fail bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.model, b.dims, b.fail = model, dims, fail
}

func (b *fakeEmbeddingBackend) snapshot() (string, int, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.model, b.dims, b.fail
}

func (b *fakeEmbeddingBackend) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/v2/embedding/check", func(w http.ResponseWriter, _ *http.Request) {
		model, dims, _ := b.snapshot()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": model, "dimensions": dims, "cached": []any{},
		})
	})

	mux.HandleFunc("/v2/embedding", func(w http.ResponseWriter, r *http.Request) {
		model, dims, fail := b.snapshot()

		if fail {
			http.Error(w, "embedding backend unavailable", http.StatusInternalServerError)

			return
		}

		var req struct {
			Items []struct {
				Hash string `json:"hash"`
				Text string `json:"text"`
			} `json:"items"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		results := make([]map[string]any, len(req.Items))
		for i, item := range req.Items {
			vec := make([]float32, dims)
			if dims > 0 {
				vec[0] = 1
			}

			results[i] = map[string]any{"hash": item.Hash, "vector": vec}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": results, "model": model, "dimensions": dims,
		})
	})

	return mux
}

// stubEmbeddingProxy is a minimal proxy.Service whose URL points at a fake
// embedding backend under test control.
type stubEmbeddingProxy struct{ baseURL string }

func (s *stubEmbeddingProxy) Start(context.Context) error { return nil }
func (s *stubEmbeddingProxy) Stop(context.Context) error  { return nil }
func (s *stubEmbeddingProxy) URL() string                 { return s.baseURL }
func (s *stubEmbeddingProxy) Ready() bool                 { return true }
func (s *stubEmbeddingProxy) RegisterToken() string       { return "" }
func (s *stubEmbeddingProxy) Invalidate()                 {}
func (s *stubEmbeddingProxy) RevokeToken()                {}

func (s *stubEmbeddingProxy) ClickHouseDatasources() []string                  { return nil }
func (s *stubEmbeddingProxy) ClickHouseDatasourceInfo() []types.DatasourceInfo { return nil }

func (s *stubEmbeddingProxy) ClickHouseQuery(context.Context, string, string, url.Values) ([]byte, error) {
	return nil, nil
}

func (s *stubEmbeddingProxy) PrometheusDatasourceInfo() []types.DatasourceInfo   { return nil }
func (s *stubEmbeddingProxy) LokiDatasourceInfo() []types.DatasourceInfo         { return nil }
func (s *stubEmbeddingProxy) BenchmarkoorDatasourceInfo() []types.DatasourceInfo { return nil }
func (s *stubEmbeddingProxy) ComputeDatasourceInfo() []types.DatasourceInfo      { return nil }
func (s *stubEmbeddingProxy) EthNodeAvailable() bool                             { return false }
func (s *stubEmbeddingProxy) EthNodeDatasourceInfo() []types.DatasourceInfo      { return nil }
func (s *stubEmbeddingProxy) EmbeddingAvailable() bool                           { return false }
func (s *stubEmbeddingProxy) EmbeddingModel() string                             { return "" }

// TestReindexPartialFailureParksEverythingAndRecovers drives the real reindex
// through a full lifecycle: a successful baseline build, a partial failure
// where one index succeeds and another fails, and a recovery once the same
// model is retried. It asserts every index — including the one that
// individually succeeded during the partial failure — ends up parked
// not-ready rather than left live in a space builtModel no longer records,
// and that a retry against the unchanged model fully restores service.
func TestReindexPartialFailureParksEverythingAndRecovers(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.PanicLevel)

	backend := &fakeEmbeddingBackend{}
	srv := httptest.NewServer(backend.handler())
	t.Cleanup(srv.Close)

	runbookReg, err := runbooks.NewRegistry(log)
	require.NoError(t, err)

	if runbookReg.Count() == 0 {
		t.Skip("no embedded runbooks available to exercise a real second index build")
	}

	r := &Runtime{
		stop: make(chan struct{}),
		log:  log,
		// Zero modules registered means GetQueryExamples returns no examples, so
		// ExampleIndex's build never touches the network and always succeeds —
		// isolating the induced failure to RunbookIndex. EIP/Specs are left nil
		// so reindex skips them.
		moduleRegistry:  module.NewRegistry(log),
		proxyService:    &stubEmbeddingProxy{baseURL: srv.URL},
		ExampleIndex:    resource.NewRefreshableExampleIndex(nil),
		RunbookRegistry: runbookReg,
		RunbookIndex:    resource.NewRefreshableRunbookIndex(nil),
	}

	const (
		modelA = "model-a"
		dims   = 4
	)

	// Baseline: a fully successful reindex at modelA.
	backend.configure(modelA, dims, false)
	r.reindex(modelA, dims, embedding.ProtocolV2)

	require.Equal(t, modelA, r.builtModel)
	require.False(t, r.reindexIncomplete)

	_, err = r.RunbookIndex.Search("finality delay", 3)
	require.NoError(t, err, "RunbookIndex should be live after a fully successful reindex")

	// Partial failure: same model, but the backend now rejects embed calls.
	// ExampleIndex has nothing to embed and "succeeds" trivially; RunbookIndex
	// has real content to embed and fails.
	backend.configure(modelA, dims, true)
	r.reindex(modelA, dims, embedding.ProtocolV2)

	require.True(t, r.reindexIncomplete, "a partial failure must mark the runtime as needing a retry")

	backend.configure(modelA, dims, false) // backend recovers; only the parked state matters now

	_, exampleErr := r.ExampleIndex.Search("anything", 3)
	require.Error(t, exampleErr, "ExampleIndex must be re-parked even though it individually succeeded this round")

	_, runbookErr := r.RunbookIndex.Search("finality delay", 3)
	require.Error(t, runbookErr, "RunbookIndex must stay parked after its own build failed")

	// The background refresher's guard: the served model is unchanged from
	// builtModel, so embeddingSpaceChanged alone would not retry — recovery
	// depends on reindexIncomplete also being checked.
	require.False(t, embeddingSpaceChanged(r.builtModel, r.builtDims, r.builtProtocol, modelA, dims, embedding.ProtocolV2))

	// Retry against the same model, as the refresher now does whenever
	// reindexIncomplete is set, regardless of embeddingSpaceChanged.
	r.reindex(modelA, dims, embedding.ProtocolV2)

	require.False(t, r.reindexIncomplete, "a fully successful retry must clear the incomplete flag")

	_, exampleErr = r.ExampleIndex.Search("anything", 3)
	require.NoError(t, exampleErr, "ExampleIndex should be live again after the retry succeeds")

	_, runbookErr = r.RunbookIndex.Search("finality delay", 3)
	require.NoError(t, runbookErr, "RunbookIndex should be live again after the retry succeeds")
}
