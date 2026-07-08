package handlers

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/attribution"
)

// capturedWorkflowRequest records what the mock upstream received.
type capturedWorkflowRequest struct {
	method string
	path   string
	rawURI string
	header http.Header
}

// newWorkflowTestHandler wires a chi router with the workflow passthrough
// pointed at upstream, mounted at /workflow/* like the proxy does. authMode and
// token select the credential behavior.
func newWorkflowTestHandler(t *testing.T, upstreamURL, authMode, token string) http.Handler {
	t.Helper()

	h, err := NewWorkflowHandler(logrus.New(), WorkflowConfig{
		URL:      upstreamURL,
		AuthMode: authMode,
		APIToken: token,
	})
	require.NoError(t, err)

	r := chi.NewRouter()
	r.Handle("/workflow", h)
	r.Handle("/workflow/*", h)

	return r
}

func TestWorkflowHandlerHeaderAllowListAndPrefixStrip(t *testing.T) {
	t.Parallel()

	var captured capturedWorkflowRequest

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = capturedWorkflowRequest{
			method: r.Method,
			path:   r.URL.Path,
			rawURI: r.URL.RequestURI(),
			header: r.Header.Clone(),
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()

	handler := newWorkflowTestHandler(t, upstream.URL, WorkflowAuthModeToken, "proxy-tok")

	req := httptest.NewRequest(http.MethodGet, "/workflow/whiteboards?limit=5", nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "idem-123")
	req.Header.Set("Last-Event-ID", "42")
	// These must be stripped; the proxy injects its own bearer in token mode.
	req.Header.Set("Authorization", "Bearer client-should-not-leak")
	req.Header.Set("Cookie", "session=nope")
	req.Header.Set(attribution.Header, "someone")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	// Prefix strip: upstream sees /api/v1/whiteboards, NOT /workflow/...
	assert.Equal(t, "/api/v1/whiteboards", captured.path)
	assert.NotContains(t, captured.path, "workflow")
	assert.Equal(t, "/api/v1/whiteboards?limit=5", captured.rawURI)

	// token mode injects the proxy-held api_token; the caller's bearer is dropped.
	assert.Equal(t, "Bearer proxy-tok", captured.header.Get("Authorization"))

	// Allow-listed headers forwarded.
	assert.Equal(t, "application/json", captured.header.Get("Accept"))
	assert.Equal(t, "application/json", captured.header.Get("Content-Type"))
	assert.Equal(t, "idem-123", captured.header.Get("Idempotency-Key"))
	assert.Equal(t, "42", captured.header.Get("Last-Event-ID"))

	// Stripped headers are gone.
	assert.Empty(t, captured.header.Get("Cookie"))
	assert.Empty(t, captured.header.Get(attribution.Header))
}

func TestWorkflowHandlerPassthroughForwardsCallerBearer(t *testing.T) {
	t.Parallel()

	var captured capturedWorkflowRequest

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = capturedWorkflowRequest{header: r.Header.Clone()}
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	// passthrough mode: no api_token; the caller's own bearer is forwarded.
	handler := newWorkflowTestHandler(t, upstream.URL, WorkflowAuthModePassthrough, "")

	req := httptest.NewRequest(http.MethodGet, "/workflow/whiteboards", nil)
	req.Header.Set("Authorization", "Bearer caller-jwt")
	req.Header.Set("Cookie", "session=nope")
	req.Header.Set(attribution.Header, "someone")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	// The caller's bearer is forwarded unchanged.
	assert.Equal(t, "Bearer caller-jwt", captured.header.Get("Authorization"))
	// Cookie and attribution are still dropped.
	assert.Empty(t, captured.header.Get("Cookie"))
	assert.Empty(t, captured.header.Get(attribution.Header))
}

func TestWorkflowHandlerRawAPIPrefixStrip(t *testing.T) {
	t.Parallel()

	var gotPath string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	handler := newWorkflowTestHandler(t, upstream.URL, WorkflowAuthModeToken, "tok")

	// The raw `api` form may include a leading /api/v1 — it must not double.
	req := httptest.NewRequest(http.MethodGet, "/workflow/api/v1/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, "/api/v1/health", gotPath)
}

func TestWorkflowHandlerEncodedSegmentPreserved(t *testing.T) {
	t.Parallel()

	var rawURI string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawURI = r.URL.RequestURI()
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	handler := newWorkflowTestHandler(t, upstream.URL, WorkflowAuthModeToken, "tok")

	// Steer key with reserved chars, percent-encoded by the caller.
	req := httptest.NewRequest(http.MethodGet,
		"/workflow/workflows/wf_1/runs/run_1/task-executions/tasks.loop.child%5Biter%3D0002%5D/queue",
		nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rawURI, "%5Biter%3D0002%5D")
}

func TestWorkflowHandlerEncodedSlashPreserved(t *testing.T) {
	t.Parallel()

	var rawURI string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawURI = r.URL.RequestURI()
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	handler := newWorkflowTestHandler(t, upstream.URL, WorkflowAuthModeToken, "tok")

	// An encoded slash inside a single id segment must NOT be decoded and
	// re-split into extra upstream segments.
	req := httptest.NewRequest(http.MethodGet, "/workflow/whiteboards/a%2Fb", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rawURI, "a%2Fb")
}

func TestWorkflowHandlerRejectsTraversal(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("upstream should not be reached for a traversal path")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	handler := newWorkflowTestHandler(t, upstream.URL, WorkflowAuthModeToken, "tok")

	cases := []struct {
		name string
		path string
	}{
		{"literal dotdot", "/workflow/../../secrets"},
		{"single-encoded dotdot", "/workflow/%2e%2e/%2e%2e/secrets"},
		{"double-encoded dotdot", "/workflow/%252e%252e/secrets"},
		{"encoded-slash dotdot", "/workflow/..%2f..%2fopenapi.yaml"},
		{"matrix-param dotdot", "/workflow/..;/secrets"},
		{"triple dot", "/workflow/.../secrets"},
		{"backslash traversal", "/workflow/..%5c..%5csecrets"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusBadRequest, rec.Code, "path %s", tc.path)
			assert.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
		})
	}
}

func TestWorkflowHandlerAPIV1PrefixNotMisStripped(t *testing.T) {
	t.Parallel()

	var gotPath string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	handler := newWorkflowTestHandler(t, upstream.URL, WorkflowAuthModeToken, "tok")

	// 'api/v1foo' is NOT the /api/v1 prefix on a segment boundary, so it must not
	// be stripped to 'foo'; it maps under the workflow root verbatim.
	req := httptest.NewRequest(http.MethodGet, "/workflow/api/v1foo", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "/api/v1/api/v1foo", gotPath)
}

func TestWorkflowHandlerRelaysStatusAndBody(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"type":"about:blank","title":"Conflict","status":409,"detail":"nope"}`)
	}))
	defer upstream.Close()

	handler := newWorkflowTestHandler(t, upstream.URL, WorkflowAuthModeToken, "tok")

	req := httptest.NewRequest(http.MethodPost, "/workflow/whiteboards/wb_1/sessions/ses_1/items", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
	assert.Contains(t, rec.Body.String(), `"detail":"nope"`)
}

func TestWorkflowHandlerRelaysPlainText404(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, "404 page not found")
	}))
	defer upstream.Close()

	handler := newWorkflowTestHandler(t, upstream.URL, WorkflowAuthModeToken, "tok")

	req := httptest.NewRequest(http.MethodGet, "/workflow/does-not-exist", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "text/plain")
	assert.Equal(t, "404 page not found", rec.Body.String())
}

func TestWorkflowHandler204NoBody(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	handler := newWorkflowTestHandler(t, upstream.URL, WorkflowAuthModeToken, "tok")

	req := httptest.NewRequest(http.MethodPost, "/workflow/workflows/wf_1/runs/run_1/cancel", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Empty(t, rec.Body.String())
}

func TestWorkflowHandlerUpstreamDownReturns502(t *testing.T) {
	t.Parallel()

	// Point at a closed server to force a dial error.
	closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closedURL := closed.URL
	closed.Close()

	handler := newWorkflowTestHandler(t, closedURL, WorkflowAuthModeToken, "tok")

	req := httptest.NewRequest(http.MethodGet, "/workflow/whiteboards", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
	assert.Contains(t, rec.Body.String(), "workflow engine is configured but unreachable")
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

func TestWorkflowHandlerSSEStreamsWithGzipRequest(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// The client requested gzip; DisableCompression must prevent transparent
		// gzip that would buffer the stream.
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
	defer upstream.Close()

	handler := newWorkflowTestHandler(t, upstream.URL, WorkflowAuthModeToken, "tok")

	req := httptest.NewRequest(http.MethodGet, "/workflow/workflows/wf_1/runs/run_1/state/stream", nil)
	req.Header.Set("Accept-Encoding", "gzip")

	fw := newFlushWriter()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(fw, req)
	}()

	// The first frame must reach us BEFORE the upstream stream is released —
	// proving it is flushed through, not buffered to EOF.
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
