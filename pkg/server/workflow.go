package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/attribution"
	"github.com/ethpandaops/panda/pkg/observability"
	"github.com/ethpandaops/panda/pkg/proxy"
	"github.com/ethpandaops/panda/pkg/workflowrelay"
)

// workflowMaxReplayBody caps how much of a request body is buffered so an auth
// retry can replay it. Beyond the cap the body streams once and the retry is
// skipped (the relay still forwards the request, just without replay).
const workflowMaxReplayBody = 4 << 20 // 4 MiB

// workflowReqData carries the per-request values the reverse-proxy Rewrite
// needs. They are resolved in serve before proxying so failures short-circuit
// cleanly; Rewrite cannot return an error. token is empty when no Authorization
// header should be sent (the NoAuthToken sentinel maps to "").
type workflowReqData struct {
	baseURL *url.URL
	token   string
	outPath string
	rawPath string
}

type workflowDataKeyType struct{}

var workflowDataKey workflowDataKeyType

// workflowPassthrough is a thin streaming relay from /api/v1/workflow/* to the
// proxy route that advertises the workflow engine. It resolves the route per
// request, forwards a strict header allow-list plus the proxy bearer, and
// streams responses (SSE-safe). The proxy owns the credential, canonical path
// clamp, and /api/v1 rooting; the relay keeps only traversal rejection.
type workflowPassthrough struct {
	log   logrus.FieldLogger
	proxy *httputil.ReverseProxy
}

// newWorkflowPassthrough builds the relay. transport is overridable for tests;
// nil uses a default compression-disabled transport (Go's automatic
// Accept-Encoding: gzip would otherwise defeat FlushInterval streaming).
func newWorkflowPassthrough(log logrus.FieldLogger, transport http.RoundTripper) *workflowPassthrough {
	if transport == nil {
		transport = &http.Transport{
			TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
			DialContext:         (&net.Dialer{Timeout: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout: 10 * time.Second,
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     90 * time.Second,
			// Disable transparent gzip: Go's Transport auto-adds
			// Accept-Encoding: gzip and decompresses, which buffers a block
			// before emitting and defeats FlushInterval: -1 for SSE.
			DisableCompression: true,
		}
	}

	wp := &workflowPassthrough{
		log: log.WithField("component", "workflow_passthrough"),
	}

	wp.proxy = &httputil.ReverseProxy{
		Transport: transport,
		// Flush every write so SSE frames reach the client immediately.
		FlushInterval: -1,
		Rewrite:       wp.rewrite,
		ErrorHandler:  wp.errorHandler,
	}

	return wp
}

// rewrite rebuilds the outbound request: the proxy route target, the explicit
// /workflow/<rest> path, the header allow-list, the injected proxy bearer, and
// the attribution header for proxy audit logging.
func (wp *workflowPassthrough) rewrite(pr *httputil.ProxyRequest) {
	data, _ := pr.In.Context().Value(workflowDataKey).(*workflowReqData)
	if data != nil {
		pr.SetURL(data.baseURL)

		// SetURL joins the inbound path onto the target, which would leak the
		// /api/v1/workflow prefix. Overwrite Path and RawPath explicitly with the
		// computed /workflow/<rest> (the proxy strips /workflow and roots at
		// /api/v1). RawPath preserves the caller's percent-encoding on the wire.
		pr.Out.URL.Path = data.outPath
		pr.Out.URL.RawPath = data.rawPath
		pr.Out.Host = pr.Out.URL.Host
	}

	// Replace the (hop-by-hop-stripped) inbound headers with the shared strict
	// allow-list. This drops Authorization, Cookie, Host, and attribution by
	// construction before we inject our own.
	allowed := workflowrelay.FilterHeaders(pr.In.Header)

	if data != nil && data.token != "" {
		allowed.Set("Authorization", "Bearer "+data.token)
	}

	if v := attribution.FromContext(pr.In.Context()); v != "" {
		allowed.Set(attribution.Header, v)
	}

	pr.Out.Header = allowed

	pr.SetXForwarded()
}

// errorHandler returns a clean 502 problem+json when the proxy is unreachable,
// distinct from the 503 no-route-advertises short-circuit in serve.
func (wp *workflowPassthrough) errorHandler(w http.ResponseWriter, _ *http.Request, err error) {
	wp.log.WithError(err).Warn("workflow proxy relay error")
	workflowrelay.WriteProblem(w, http.StatusBadGateway, "Bad Gateway", "proxy is unreachable")
}

// serve relays the request to the resolved proxy route, recording a single
// passthrough metric once the (possibly long-lived) response completes. On a
// proxy auth rejection it invalidates the token and replays once.
func (wp *workflowPassthrough) serve(w http.ResponseWriter, r *http.Request, route proxy.Service) {
	baseURL, err := url.Parse(strings.TrimRight(route.URL(), "/"))
	if err != nil || baseURL.Scheme == "" || baseURL.Host == "" {
		observability.WorkflowPassthroughTotal.WithLabelValues(r.Method, "5xx").Inc()
		workflowrelay.WriteProblem(w, http.StatusBadGateway, "Bad Gateway", "proxy is unreachable")

		return
	}

	outPath, rawPath, err := workflowOutboundPath(chi.URLParam(r, "*"))
	if err != nil {
		observability.WorkflowPassthroughTotal.WithLabelValues(r.Method, "4xx").Inc()
		workflowrelay.WriteProblem(w, http.StatusBadRequest, "Bad Request", err.Error())

		return
	}

	// Preserve a base path on the proxy URL (a proxy mounted under a subpath),
	// matching the string-concat join every other proxy call uses; the rewrite
	// overwrites Path/RawPath wholesale, so the prefix must be baked in here.
	if basePath := baseURL.Path; basePath != "" && basePath != "/" {
		outPath = basePath + outPath
		rawPath = baseURL.EscapedPath() + rawPath
	}

	// Buffer the request body so an auth retry can replay it. Beyond the cap the
	// body streams once and the retry is skipped.
	buffered, replayable, firstBody, err := bufferWorkflowBody(r.Body)
	if err != nil {
		observability.WorkflowPassthroughTotal.WithLabelValues(r.Method, "5xx").Inc()
		workflowrelay.WriteProblem(w, http.StatusBadGateway, "Bad Gateway", "proxy is unreachable")

		return
	}

	data := &workflowReqData{
		baseURL: baseURL,
		token:   routeBearer(route),
		outPath: outPath,
		rawPath: rawPath,
	}
	ctx := context.WithValue(r.Context(), workflowDataKey, data)

	// Long-lived SSE streams must not hit a write deadline. Clear it defensively
	// (the recorder's Unwrap lets the controller reach the real writer) so a
	// future server WriteTimeout cannot truncate a stream mid-flight.
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})

	runOnce := func(trap bool, body io.ReadCloser) *workflowStatusRecorder {
		// Clear any response headers a prior (trapped) attempt copied in, so a
		// retried response never carries stale upstream headers.
		h := w.Header()
		for k := range h {
			delete(h, k)
		}

		rec := &workflowStatusRecorder{ResponseWriter: w, status: http.StatusOK, trap: trap}

		req := r.Clone(ctx)
		req.Body = body

		wp.proxy.ServeHTTP(rec, req)

		return rec
	}

	start := time.Now()

	rec := runOnce(replayable, firstBody)

	if rec.trapped {
		// A 401/403 reached the relay: invalidate, refresh the bearer, and replay
		// once; a second rejection is relayed to the caller. The rejection may
		// also be an engine 401 the proxy relayed verbatim — indistinguishable at
		// this hop without coupling to the proxy's error shape (which older
		// deployed proxies would not honor). That is deliberate: in passthrough
		// mode a refreshed user token is exactly the right recovery, and in token
		// mode the cost is one bounded token refresh before the 401 is relayed.
		route.Invalidate()
		data.token = routeBearer(route)

		rec = runOnce(false, replayWorkflowBody(buffered))
	}

	observability.WorkflowPassthroughTotal.
		WithLabelValues(r.Method, workflowStatusClass(rec.status)).Inc()

	// Skip the duration histogram for SSE responses: ServeHTTP blocks for the
	// whole stream lifetime, so time.Since(start) is the stream duration, not
	// request latency, and would swamp the +Inf bucket. The counter above still
	// covers every response.
	if !workflowrelay.IsEventStream(rec.Header().Get("Content-Type")) {
		observability.WorkflowPassthroughDuration.
			WithLabelValues(r.Method).Observe(time.Since(start).Seconds())
	}
}

// bufferWorkflowBody reads the request body up to the replay cap. When the body
// fits, it is buffered and replayable; beyond the cap the first attempt streams
// the whole body once (buffered prefix + remaining reader) and replay is off.
func bufferWorkflowBody(body io.ReadCloser) (buffered []byte, replayable bool, firstBody io.ReadCloser, err error) {
	if body == nil {
		return nil, true, http.NoBody, nil
	}

	buf, err := io.ReadAll(io.LimitReader(body, workflowMaxReplayBody+1))
	if err != nil {
		_ = body.Close()

		return nil, false, nil, err
	}

	if len(buf) > workflowMaxReplayBody {
		// Too large to buffer for replay: stream the whole body once, no retry.
		combined := struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(buf), body), body}

		return nil, false, combined, nil
	}

	_ = body.Close()

	return buf, true, io.NopCloser(bytes.NewReader(buf)), nil
}

// replayWorkflowBody returns a fresh reader over the buffered body for a retry.
func replayWorkflowBody(buffered []byte) io.ReadCloser {
	if buffered == nil {
		return http.NoBody
	}

	return io.NopCloser(bytes.NewReader(buffered))
}

// routeBearer returns the Authorization bearer for the proxy route, or "" when
// no Authorization header should be sent (no auth or an unavailable token). The
// NoAuthToken sentinel is never forwarded as a credential.
func routeBearer(route proxy.Service) string {
	tok := route.RegisterToken()
	if tok == "" || tok == proxy.NoAuthToken {
		return ""
	}

	return tok
}

// workflowRoute resolves the proxy route that advertises the workflow engine.
// With a router it uses the priority-ordered WorkflowRoute selection (shared
// with WorkflowInfo so links and traffic never disagree); with a single client
// it returns that client when it advertises the engine directly.
func (s *service) workflowRoute() (proxy.Service, bool) {
	if s.proxyService == nil {
		return nil, false
	}

	if router, ok := s.proxyService.(proxy.Router); ok {
		client, found := router.WorkflowRoute()
		if !found {
			return nil, false
		}

		return client, true
	}

	if provider, ok := s.proxyService.(proxy.WorkflowInfoProvider); ok {
		if enabled, _ := provider.WorkflowInfo(); enabled {
			return s.proxyService, true
		}
	}

	return nil, false
}

// workflowInfo reports whether a proxy advertises the workflow engine and its
// web URL, read from proxy discovery.
func (s *service) workflowInfo() (enabled bool, webURL string) {
	if s.proxyService == nil {
		return false, ""
	}

	if provider, ok := s.proxyService.(proxy.WorkflowInfoProvider); ok {
		return provider.WorkflowInfo()
	}

	return false, ""
}

// handleAPIWorkflowProxy handles /api/v1/workflow/* — the streaming relay to
// the proxy route that advertises the workflow engine. When no proxy advertises
// it, it short-circuits with a 503 before any relaying.
func (s *service) handleAPIWorkflowProxy(w http.ResponseWriter, r *http.Request) {
	route, ok := s.workflowRoute()
	if !ok {
		// No route short-circuit: count it (5xx) so every inbound request is
		// reflected in the passthrough total.
		observability.WorkflowPassthroughTotal.WithLabelValues(r.Method, "5xx").Inc()
		workflowrelay.WriteProblem(w, http.StatusServiceUnavailable, "Service Unavailable",
			"workflow engine is not available: no configured proxy advertises it")

		return
	}

	s.workflow.serve(w, r, route)
}

// workflowOutboundPath builds the outbound relay path pair (decoded Path,
// encoded RawPath) rooted at /workflow, forwarding <rest> verbatim. The
// canonical clamp and /api/v1 rooting live proxy-side; the server keeps only
// traversal rejection on the decoded form.
func workflowOutboundPath(rest string) (outPath, rawPath string, err error) {
	decoded, decErr := url.PathUnescape(rest)
	if decErr != nil {
		return "", "", errors.New("invalid path encoding")
	}

	if traversalErr := workflowrelay.RejectTraversal(decoded); traversalErr != nil {
		return "", "", traversalErr
	}

	rest = strings.TrimPrefix(rest, "/")
	decoded = strings.TrimPrefix(decoded, "/")

	return "/workflow/" + decoded, "/workflow/" + rest, nil
}

// workflowStatusClass maps an HTTP status code to a coarse class label.
func workflowStatusClass(status int) string {
	switch {
	case status >= 500:
		return "5xx"
	case status >= 400:
		return "4xx"
	case status >= 300:
		return "3xx"
	case status >= 200:
		return "2xx"
	default:
		return "1xx"
	}
}

// workflowStatusRecorder captures the response status for metrics while
// preserving streaming semantics (Flush) and ResponseController compatibility
// (Unwrap). In trap mode it swallows a proxy auth rejection (401/403) so the
// relay can invalidate the token and replay before any bytes reach the client.
type workflowStatusRecorder struct {
	http.ResponseWriter
	status  int
	wrote   bool
	trap    bool
	trapped bool
}

func (s *workflowStatusRecorder) WriteHeader(code int) {
	if s.trap && !s.trapped && (code == http.StatusUnauthorized || code == http.StatusForbidden) {
		s.trapped = true
		s.status = code

		return
	}

	if s.trapped {
		return
	}

	if !s.wrote {
		s.status = code
		s.wrote = true
	}

	s.ResponseWriter.WriteHeader(code)
}

func (s *workflowStatusRecorder) Write(b []byte) (int, error) {
	if s.trapped {
		// Discard the trapped rejection body; the retry writes the real response.
		return len(b), nil
	}

	if !s.wrote {
		s.status = http.StatusOK
		s.wrote = true
	}

	return s.ResponseWriter.Write(b)
}

func (s *workflowStatusRecorder) Flush() {
	if s.trapped {
		return
	}

	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap exposes the underlying writer to http.ResponseController so
// ReverseProxy's flushing (via the controller) reaches the real writer.
func (s *workflowStatusRecorder) Unwrap() http.ResponseWriter {
	return s.ResponseWriter
}
