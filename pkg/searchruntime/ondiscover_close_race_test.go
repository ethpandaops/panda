package searchruntime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/types"
)

// noEmbeddingProxyService is a minimal proxy.Service whose embedding probes
// always fail fast (a 404 from a real local server, not a hang), so
// Runtime.activate reliably takes its "embedding not available" early-return
// path on every call. That makes OnDiscover's Add/Done cycle fast enough to
// hammer repeatedly in a race test.
type noEmbeddingProxyService struct {
	url string
}

func newNoEmbeddingProxyService(t *testing.T) *noEmbeddingProxyService {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	return &noEmbeddingProxyService{url: srv.URL}
}

func (f *noEmbeddingProxyService) Start(context.Context) error { return nil }
func (f *noEmbeddingProxyService) Stop(context.Context) error  { return nil }
func (f *noEmbeddingProxyService) URL() string                 { return f.url }
func (f *noEmbeddingProxyService) Ready() bool                 { return true }
func (f *noEmbeddingProxyService) RegisterToken() string       { return "" }
func (f *noEmbeddingProxyService) Invalidate()                 {}
func (f *noEmbeddingProxyService) RevokeToken()                {}

func (f *noEmbeddingProxyService) ClickHouseDatasources() []string                  { return nil }
func (f *noEmbeddingProxyService) ClickHouseDatasourceInfo() []types.DatasourceInfo { return nil }

func (f *noEmbeddingProxyService) ClickHouseQuery(context.Context, string, string, url.Values) ([]byte, error) {
	return nil, nil
}

func (f *noEmbeddingProxyService) PrometheusDatasourceInfo() []types.DatasourceInfo   { return nil }
func (f *noEmbeddingProxyService) LokiDatasourceInfo() []types.DatasourceInfo         { return nil }
func (f *noEmbeddingProxyService) BenchmarkoorDatasourceInfo() []types.DatasourceInfo { return nil }
func (f *noEmbeddingProxyService) ComputeDatasourceInfo() []types.DatasourceInfo      { return nil }
func (f *noEmbeddingProxyService) EthNodeAvailable() bool                             { return false }
func (f *noEmbeddingProxyService) EthNodeDatasourceInfo() []types.DatasourceInfo      { return nil }
func (f *noEmbeddingProxyService) EmbeddingAvailable() bool                           { return false }
func (f *noEmbeddingProxyService) EmbeddingModel() string                             { return "" }

// TestOnDiscoverNeverRacesClose hammers concurrent OnDiscover and Close calls
// across many trials. Before the fix, a discovery goroutine calling
// wg.Add(1) could land while Close's wg.Wait() was already unblocking,
// which the sync.WaitGroup docs call misuse and Go's runtime turns into a
// panic. This must run with -race for the assertion to mean anything.
func TestOnDiscoverNeverRacesClose(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.PanicLevel)

	const trials = 200

	for i := 0; i < trials; i++ {
		proxySvc := newNoEmbeddingProxyService(t)
		r := &Runtime{
			log:          log,
			proxyService: proxySvc,
			stop:         make(chan struct{}),
		}

		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()

			for j := 0; j < 5; j++ {
				r.OnDiscover()
			}
		}()

		go func() {
			defer wg.Done()

			time.Sleep(time.Millisecond)
			require.NoError(t, r.Close())
		}()

		wg.Wait()
	}
}
