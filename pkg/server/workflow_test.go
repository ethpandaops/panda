package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/observability"
	"github.com/ethpandaops/panda/pkg/proxy"
	"github.com/ethpandaops/panda/pkg/types"
)

// fakeProxy is a minimal proxy.Service + proxy.WorkflowInfoProvider used to
// stand in for the resolved workflow route. RegisterToken walks the tokens
// slice as Invalidate is called, so an auth retry sees a fresh bearer.
type fakeProxy struct {
	url     string
	enabled bool
	webURL  string
	tokens  []string

	mu          sync.Mutex
	invalidated int
}

func (f *fakeProxy) URL() string { return f.url }

func (f *fakeProxy) RegisterToken() string {
	f.mu.Lock()
	defer f.mu.Unlock()

	if len(f.tokens) == 0 {
		return proxy.NoAuthToken
	}

	i := f.invalidated
	if i >= len(f.tokens) {
		i = len(f.tokens) - 1
	}

	return f.tokens[i]
}

func (f *fakeProxy) Invalidate() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.invalidated++
}

func (f *fakeProxy) invalidateCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.invalidated
}

func (f *fakeProxy) WorkflowInfo() (bool, string) { return f.enabled, f.webURL }

// The rest of proxy.Service is unused by the relay.
func (f *fakeProxy) Start(context.Context) error                      { return nil }
func (f *fakeProxy) Stop(context.Context) error                       { return nil }
func (f *fakeProxy) Ready() bool                                      { return true }
func (f *fakeProxy) RevokeToken()                                     {}
func (f *fakeProxy) ClickHouseDatasources() []string                  { return nil }
func (f *fakeProxy) ClickHouseDatasourceInfo() []types.DatasourceInfo { return nil }
func (f *fakeProxy) ClickHouseQuery(context.Context, string, string, url.Values) ([]byte, error) {
	return nil, nil
}
func (f *fakeProxy) PrometheusDatasourceInfo() []types.DatasourceInfo   { return nil }
func (f *fakeProxy) LokiDatasourceInfo() []types.DatasourceInfo         { return nil }
func (f *fakeProxy) BenchmarkoorDatasourceInfo() []types.DatasourceInfo { return nil }
func (f *fakeProxy) ComputeDatasourceInfo() []types.DatasourceInfo      { return nil }
func (f *fakeProxy) EthNodeAvailable() bool                             { return false }
func (f *fakeProxy) EthNodeDatasourceInfo() []types.DatasourceInfo      { return nil }
func (f *fakeProxy) EmbeddingAvailable() bool                           { return false }
func (f *fakeProxy) EmbeddingModel() string                             { return "" }

var (
	_ proxy.Service              = (*fakeProxy)(nil)
	_ proxy.WorkflowInfoProvider = (*fakeProxy)(nil)
)

// capturedRequest records what the mock proxy received from the relay.
type capturedRequest struct {
	method string
	path   string
	rawURI string
	header http.Header
}

// newWorkflowHandler mounts the relay on a chi router pointed at proxyURL with
// the given proxy-bearer tokens, matching how the server wires the route.
func newWorkflowHandler(proxyURL string, tokens []string) http.Handler {
	s := &service{
		proxyService: &fakeProxy{url: proxyURL, enabled: true, tokens: tokens},
		workflow:     newWorkflowPassthrough(logrus.New(), nil),
	}

	r := chi.NewRouter()
	r.Use(attributionMiddleware)
	r.HandleFunc("/api/v1/workflow", s.handleAPIWorkflowProxy)
	r.HandleFunc("/api/v1/workflow/*", s.handleAPIWorkflowProxy)

	return r
}

func TestWorkflowRelayTargetsRouteAndInjectsBearer(t *testing.T) {
	t.Parallel()

	var captured capturedRequest

	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = capturedRequest{
			method: r.Method,
			path:   r.URL.Path,
			rawURI: r.URL.RequestURI(),
			header: r.Header.Clone(),
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer proxySrv.Close()

	handler := newWorkflowHandler(proxySrv.URL, []string{"server-tok"})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/workflow/whiteboards?limit=5", nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "idem-123")
	req.Header.Set("Last-Event-ID", "42")
	// These must be stripped before the relay injects its own bearer.
	req.Header.Set("Authorization", "Bearer client-should-not-leak")
	req.Header.Set("Cookie", "session=nope")
	// Caller attribution is lifted into context by attributionMiddleware and
	// re-added for proxy audit.
	req.Header.Set("X-Panda-On-Behalf-Of", "someone")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	// Targets <route>/workflow/<rest>, <rest> verbatim; the /api/v1/workflow
	// prefix does not leak.
	assert.Equal(t, "/workflow/whiteboards", captured.path)
	assert.Equal(t, "/workflow/whiteboards?limit=5", captured.rawURI)

	// The relay injects its own proxy bearer; the client's is dropped.
	assert.Equal(t, "Bearer server-tok", captured.header.Get("Authorization"))

	// Allow-listed headers forwarded.
	assert.Equal(t, "application/json", captured.header.Get("Accept"))
	assert.Equal(t, "application/json", captured.header.Get("Content-Type"))
	assert.Equal(t, "idem-123", captured.header.Get("Idempotency-Key"))
	assert.Equal(t, "42", captured.header.Get("Last-Event-ID"))

	// Cookie stripped; attribution forwarded for proxy audit.
	assert.Empty(t, captured.header.Get("Cookie"))
	assert.Equal(t, "someone", captured.header.Get("X-Panda-On-Behalf-Of"))
}

func TestWorkflowRelayPreservesProxyBasePath(t *testing.T) {
	t.Parallel()

	var captured capturedRequest

	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = capturedRequest{path: r.URL.Path, rawURI: r.URL.RequestURI()}

		_, _ = io.WriteString(w, `{}`)
	}))
	defer proxySrv.Close()

	// A proxy mounted under a subpath: the relay must join /workflow onto the
	// base path (like every other proxy call), not overwrite it away.
	handler := newWorkflowHandler(proxySrv.URL+"/sub/mount/", []string{"tok"})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/workflow/whiteboards?limit=5", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "/sub/mount/workflow/whiteboards", captured.path)
	assert.Equal(t, "/sub/mount/workflow/whiteboards?limit=5", captured.rawURI)
}

func TestWorkflowRelayBareRouteRelaysEngineRoot(t *testing.T) {
	t.Parallel()

	var captured capturedRequest

	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = capturedRequest{path: r.URL.Path}

		_, _ = io.WriteString(w, `{}`)
	}))
	defer proxySrv.Close()

	handler := newWorkflowHandler(proxySrv.URL, []string{"tok"})

	// `panda workflow api GET /` produces the bare /api/v1/workflow path; it
	// must relay to the engine root, not 404.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/workflow", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "/workflow/", captured.path)
}

func TestWorkflowRelayNoAuthTokenSendsNoAuthorization(t *testing.T) {
	t.Parallel()

	var (
		gotAuth string
		seen    bool
	)

	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		seen = true

		w.WriteHeader(http.StatusOK)
	}))
	defer proxySrv.Close()

	// The NoAuthToken sentinel maps to no Authorization header.
	handler := newWorkflowHandler(proxySrv.URL, []string{proxy.NoAuthToken})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/workflow/whiteboards", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, seen)
	assert.Empty(t, gotAuth, "NoAuthToken must not be forwarded as a credential")
}

func TestWorkflowRelayEncodedSegmentPreserved(t *testing.T) {
	t.Parallel()

	var rawURI string

	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawURI = r.URL.RequestURI()
		w.WriteHeader(http.StatusOK)
	}))
	defer proxySrv.Close()

	handler := newWorkflowHandler(proxySrv.URL, []string{"tok"})

	// Steer key with reserved chars, percent-encoded by the caller — forwarded
	// verbatim (the proxy owns the canonical clamp).
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/workflow/workflows/wf_1/runs/run_1/task-executions/tasks.loop.child%5Biter%3D0002%5D/queue",
		nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rawURI, "%5Biter%3D0002%5D")
}

func TestWorkflowRelayEncodedSlashPreserved(t *testing.T) {
	t.Parallel()

	var rawURI string

	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawURI = r.URL.RequestURI()
		w.WriteHeader(http.StatusOK)
	}))
	defer proxySrv.Close()

	handler := newWorkflowHandler(proxySrv.URL, []string{"tok"})

	// An encoded slash inside a single id segment must survive on the wire, not
	// be decoded and re-split into extra segments.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/workflow/whiteboards/a%2Fb", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rawURI, "a%2Fb")
}

func TestWorkflowRelayRejectsTraversal(t *testing.T) {
	t.Parallel()

	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("proxy should not be reached for a traversal path")
		w.WriteHeader(http.StatusOK)
	}))
	defer proxySrv.Close()

	handler := newWorkflowHandler(proxySrv.URL, []string{"tok"})

	cases := []struct {
		name string
		path string
	}{
		{"literal dotdot", "/api/v1/workflow/../../secrets"},
		{"single-encoded dotdot", "/api/v1/workflow/%2e%2e/%2e%2e/secrets"},
		{"double-encoded dotdot", "/api/v1/workflow/%252e%252e/secrets"},
		{"encoded-slash dotdot", "/api/v1/workflow/..%2f..%2fopenapi.yaml"},
		{"matrix-param dotdot", "/api/v1/workflow/..;/secrets"},
		{"triple dot", "/api/v1/workflow/.../secrets"},
		{"backslash traversal", "/api/v1/workflow/..%5c..%5csecrets"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusBadRequest, rec.Code, "path %s", tc.path)
			assert.Contains(t, rec.Body.String(), "path traversal is not allowed")
		})
	}
}

func TestWorkflowRelayRelaysStatusAndBody(t *testing.T) {
	t.Parallel()

	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"type":"about:blank","title":"Conflict","status":409,"detail":"nope"}`)
	}))
	defer proxySrv.Close()

	handler := newWorkflowHandler(proxySrv.URL, []string{"tok"})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/workflow/whiteboards/wb_1/sessions/ses_1/items", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
	assert.Contains(t, rec.Body.String(), `"detail":"nope"`)
}

func TestWorkflowRelayRelaysPlainText404(t *testing.T) {
	t.Parallel()

	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, "404 page not found")
	}))
	defer proxySrv.Close()

	handler := newWorkflowHandler(proxySrv.URL, []string{"tok"})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/workflow/does-not-exist", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "text/plain")
	assert.Equal(t, "404 page not found", rec.Body.String())
}

func TestWorkflowRelay204NoBody(t *testing.T) {
	t.Parallel()

	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer proxySrv.Close()

	handler := newWorkflowHandler(proxySrv.URL, []string{"tok"})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/workflow/workflows/wf_1/runs/run_1/cancel", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Empty(t, rec.Body.String())
}

func TestWorkflowRelayNoRouteReturns503(t *testing.T) {
	t.Parallel()

	// No proxy advertises the engine.
	s := &service{
		proxyService: &fakeProxy{url: "http://127.0.0.1:0", enabled: false},
		workflow:     newWorkflowPassthrough(logrus.New(), nil),
	}

	r := chi.NewRouter()
	r.HandleFunc("/api/v1/workflow/*", s.handleAPIWorkflowProxy)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/workflow/whiteboards", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
	assert.Contains(t, rec.Body.String(),
		"workflow engine is not available: no configured proxy advertises it")
}

func TestWorkflowRelayProxyUnreachableReturns502(t *testing.T) {
	t.Parallel()

	// Point at a closed server to force a dial error.
	closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closedURL := closed.URL
	closed.Close()

	handler := newWorkflowHandler(closedURL, []string{"tok"})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/workflow/whiteboards", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
	assert.Contains(t, rec.Body.String(), "proxy is unreachable")
}

func TestWorkflowRelayAuthRetryReplaysBody(t *testing.T) {
	t.Parallel()

	var (
		mu     sync.Mutex
		bodies []string
	)

	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		mu.Lock()
		bodies = append(bodies, string(body))
		mu.Unlock()

		// The stale bearer is rejected; a fresh one (after invalidate) succeeds.
		if r.Header.Get("Authorization") != "Bearer good-tok" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"detail":"stale"}`)

			return
		}

		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer proxySrv.Close()

	fp := &fakeProxy{url: proxySrv.URL, enabled: true, tokens: []string{"bad-tok", "good-tok"}}
	s := &service{proxyService: fp, workflow: newWorkflowPassthrough(logrus.New(), nil)}

	r := chi.NewRouter()
	r.HandleFunc("/api/v1/workflow/*", s.handleAPIWorkflowProxy)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/workflow/whiteboards/wb_1/sessions",
		strings.NewReader("payload"))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"ok":true`)
	assert.Equal(t, 1, fp.invalidateCount(), "token invalidated exactly once")

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, bodies, 2, "request attempted twice (original + retry)")
	assert.Equal(t, "payload", bodies[0])
	assert.Equal(t, "payload", bodies[1], "body replayed on retry")
}

func TestWorkflowRelayAuthRetryRelaysFinal401(t *testing.T) {
	t.Parallel()

	var (
		mu    sync.Mutex
		calls int
	)

	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()

		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"detail":"proxy credential rejected"}`)
	}))
	defer proxySrv.Close()

	fp := &fakeProxy{url: proxySrv.URL, enabled: true, tokens: []string{"bad-1", "bad-2"}}
	s := &service{proxyService: fp, workflow: newWorkflowPassthrough(logrus.New(), nil)}

	r := chi.NewRouter()
	r.HandleFunc("/api/v1/workflow/*", s.handleAPIWorkflowProxy)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/workflow/whiteboards", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	// After a failed retry, the proxy's 401 is relayed verbatim.
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, rec.Body.String(), "proxy credential rejected")
	assert.Equal(t, 1, fp.invalidateCount())

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 2, calls, "attempted twice then relayed")
}

func TestWorkflowRelayAuthRetryPreservesMiddlewareHeaders(t *testing.T) {
	t.Parallel()

	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good-tok" {
			// Set a header only the rejected attempt carries: the retry must
			// reset it rather than leak it into the final response.
			w.Header().Set("X-Stale-Upstream", "yes")
			w.WriteHeader(http.StatusUnauthorized)

			return
		}

		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer proxySrv.Close()

	fp := &fakeProxy{url: proxySrv.URL, enabled: true, tokens: []string{"bad-tok", "good-tok"}}
	s := &service{proxyService: fp, workflow: newWorkflowPassthrough(logrus.New(), nil)}

	r := chi.NewRouter()
	// A middleware-set response header is the pre-relay baseline: the retry's
	// header reset must restore it, not strip it along with the stale ones.
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			w.Header().Set("X-Middleware", "kept")
			next.ServeHTTP(w, req)
		})
	})
	r.HandleFunc("/api/v1/workflow/*", s.handleAPIWorkflowProxy)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/workflow/whiteboards", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "kept", rec.Header().Get("X-Middleware"),
		"middleware header survives the auth-retry header reset")
	assert.Empty(t, rec.Header().Get("X-Stale-Upstream"),
		"trapped attempt's upstream header does not leak into the retried response")
}

// flushWriter is an http.ResponseWriter that records flushes so we can assert
// SSE frames are streamed, not buffered to the end.
type flushWriter struct {
	mu      sync.Mutex
	header  http.Header
	buf     strings.Builder
	flushes int
	status  int
	flushed chan struct{}
	once    sync.Once
}

func newFlushWriter() *flushWriter {
	return &flushWriter{header: make(http.Header), flushed: make(chan struct{})}
}

func (f *flushWriter) Header() http.Header { return f.header }

func (f *flushWriter) WriteHeader(code int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = code
}

func (f *flushWriter) Write(b []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.buf.Write(b)
}

func (f *flushWriter) Flush() {
	f.mu.Lock()
	n := f.buf.Len()
	f.flushes++
	f.mu.Unlock()

	if n > 0 {
		f.once.Do(func() { close(f.flushed) })
	}
}

func (f *flushWriter) body() string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.buf.String()
}

func TestWorkflowRelaySSEStreamsWithGzipRequest(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})

	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// DisableCompression must prevent transparent gzip that would buffer.
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)

		flusher, ok := w.(http.Flusher)
		require.True(t, ok)

		_, _ = io.WriteString(w, "data: first\n\n")
		flusher.Flush()

		<-release // hold the stream open until the test has seen the first frame

		_, _ = io.WriteString(w, "data: second\n\n")
		flusher.Flush()
	}))
	defer proxySrv.Close()

	handler := newWorkflowHandler(proxySrv.URL, []string{"tok"})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/workflow/workflows/wf_1/runs/run_1/state/stream", nil)
	req.Header.Set("Accept-Encoding", "gzip")

	fw := newFlushWriter()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(fw, req)
	}()

	// The first frame must reach us BEFORE the proxy stream is released, proving
	// it is flushed through, not buffered to EOF.
	select {
	case <-fw.flushed:
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("SSE first frame was not flushed before stream completion")
	}

	assert.Contains(t, fw.body(), "data: first")

	close(release)
	<-done

	assert.Equal(t, "text/event-stream", fw.Header().Get("Content-Type"))
	assert.Equal(t, "no", fw.Header().Get("X-Accel-Buffering"))
	assert.Contains(t, fw.body(), "data: second")
}

// TestWorkflowRelayMetricEmittedOnceOnStreamCompletion asserts the relay emits
// its request counter exactly once, and only after a long-lived SSE stream
// completes — not once per flushed frame — and that the SSE duration is excluded
// from the histogram. It uses PATCH (a real method no other workflow test
// exercises) so the (method, status_class) series is not shared.
func TestWorkflowRelayMetricEmittedOnceOnStreamCompletion(t *testing.T) {
	release := make(chan struct{})

	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		flusher, ok := w.(http.Flusher)
		require.True(t, ok)

		_, _ = io.WriteString(w, "data: first\n\n")
		flusher.Flush()
		<-release
		_, _ = io.WriteString(w, "data: second\n\n")
		flusher.Flush()
	}))
	defer proxySrv.Close()

	handler := newWorkflowHandler(proxySrv.URL, []string{"tok"})

	const method = http.MethodPatch

	before := metricCount(t, method, "2xx")
	beforeDuration := durationSampleCount(t, method)

	req := httptest.NewRequest(method, "/api/v1/workflow/workflows/wf_1/runs/run_1/state/stream", nil)
	fw := newFlushWriter()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(fw, req)
	}()

	// While the stream is still open the counter must not have incremented — it
	// fires on completion, not per frame.
	select {
	case <-fw.flushed:
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("SSE first frame was not flushed")
	}

	assert.InDelta(t, before, metricCount(t, method, "2xx"), 0.0001,
		"metric must not increment mid-stream")

	close(release)
	<-done

	assert.InDelta(t, before+1, metricCount(t, method, "2xx"), 0.0001,
		"metric should increment exactly once on stream completion")

	// SSE is excluded from the duration histogram.
	assert.Equal(t, beforeDuration, durationSampleCount(t, method),
		"SSE responses must not be recorded in the duration histogram")
}

func TestWorkflowRelayMetricEmittedOnce(t *testing.T) {
	t.Parallel()

	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))
	defer proxySrv.Close()

	handler := newWorkflowHandler(proxySrv.URL, []string{"tok"})

	// DELETE: a real method no other workflow test exercises, so this (method,
	// status_class) series is not shared with parallel tests (the registry is
	// process-global).
	const method = http.MethodDelete

	before := metricCount(t, method, "2xx")
	beforeDuration := durationSampleCount(t, method)

	req := httptest.NewRequest(method, "/api/v1/workflow/whiteboards/wb_1/sessions/ses_1/queue/item1", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.InDelta(t, before+1, metricCount(t, method, "2xx"), 0.0001,
		"metric should increment exactly once")
	// A non-SSE response IS recorded in the duration histogram.
	assert.Equal(t, beforeDuration+1, durationSampleCount(t, method),
		"non-SSE response recorded in duration histogram")
}

func TestWorkflowRelayShortCircuitsAreCounted(t *testing.T) {
	// 503 (no route) and 400 (traversal) short-circuits are still counted. Uses
	// custom methods so the series are not shared with other tests.
	noRoute := &service{
		proxyService: &fakeProxy{enabled: false},
		workflow:     newWorkflowPassthrough(logrus.New(), nil),
	}

	r := chi.NewRouter()
	r.HandleFunc("/api/v1/workflow/*", noRoute.handleAPIWorkflowProxy)

	// OPTIONS and PUT are standard methods no other workflow test exercises, so
	// these (method, status_class) series are not shared with parallel tests.
	before503 := metricCount(t, http.MethodOptions, "5xx")
	r.ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodOptions, "/api/v1/workflow/whiteboards", nil))
	assert.InDelta(t, before503+1, metricCount(t, http.MethodOptions, "5xx"), 0.0001,
		"503 short-circuit counted")

	handler := newWorkflowHandler("http://127.0.0.1:0", []string{"tok"})
	before400 := metricCount(t, http.MethodPut, "4xx")
	handler.ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodPut, "/api/v1/workflow/../secrets", nil))
	assert.InDelta(t, before400+1, metricCount(t, http.MethodPut, "4xx"), 0.0001,
		"400 short-circuit counted")
}

func TestWorkflowInfoReflectsDiscovery(t *testing.T) {
	t.Parallel()

	s := &service{proxyService: &fakeProxy{enabled: true, webURL: "https://workflow.example.io"}}

	rec := httptest.NewRecorder()
	s.handleAPIWorkflowInfo(rec, httptest.NewRequest(http.MethodGet, "/api/v1/workflow-info", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"enabled":true,"web_base_url":"https://workflow.example.io"}`, rec.Body.String())
}

func TestWorkflowInfoEmptyWhenAbsent(t *testing.T) {
	t.Parallel()

	s := &service{proxyService: &fakeProxy{enabled: false}}

	rec := httptest.NewRecorder()
	s.handleAPIWorkflowInfo(rec, httptest.NewRequest(http.MethodGet, "/api/v1/workflow-info", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{}`, rec.Body.String())
}

// metricCount reads the current value of the workflow passthrough counter for
// the given method and status class.
func metricCount(t *testing.T, method, statusClass string) float64 {
	t.Helper()

	c, err := observability.WorkflowPassthroughTotal.GetMetricWithLabelValues(method, statusClass)
	require.NoError(t, err)

	return testutil.ToFloat64(c)
}

// durationSampleCount reports the number of observations recorded in the
// workflow passthrough duration histogram for the given method.
func durationSampleCount(t *testing.T, method string) uint64 {
	t.Helper()

	obs, err := observability.WorkflowPassthroughDuration.GetMetricWithLabelValues(method)
	require.NoError(t, err)

	metric, ok := obs.(prometheus.Metric)
	require.True(t, ok)

	var m dto.Metric
	require.NoError(t, metric.Write(&m))

	return m.GetHistogram().GetSampleCount()
}
