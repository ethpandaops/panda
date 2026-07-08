package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/attribution"
	"github.com/ethpandaops/panda/pkg/workflowrelay"
)

// Workflow auth modes select how the proxy credentials the upstream engine.
const (
	// WorkflowAuthModeToken injects the proxy-held api_token as the bearer; the
	// inbound proxy bearer never reaches upstream.
	WorkflowAuthModeToken = "token"
	// WorkflowAuthModePassthrough re-attaches the caller's own bearer unchanged
	// so the engine validates the user's token directly.
	WorkflowAuthModePassthrough = "passthrough"
)

// WorkflowConfig holds the workflow-engine passthrough handler configuration.
type WorkflowConfig struct {
	// URL is the workflow engine API origin (bare scheme+host[:port], no path).
	URL string
	// AuthMode is "token" or "passthrough".
	AuthMode string
	// APIToken is the bearer injected in "token" mode.
	APIToken string
}

// workflowReqData carries the per-request path values the reverse-proxy Rewrite
// needs. They are resolved (and validated) in ServeHTTP before proxying so
// failures short-circuit cleanly; Rewrite cannot return an error.
type workflowReqData struct {
	outPath string
	rawPath string
}

type workflowDataKeyType struct{}

var workflowDataKey workflowDataKeyType

// WorkflowHandler is a thin credentialed reverse proxy to the workflow engine
// API. It clamps and cleans the request path, forwards a strict header
// allow-list, sets Authorization per auth mode, and streams responses
// (SSE-safe). All HTTP methods are forwarded: the engine manages mutable
// resources and authorizes each request itself.
type WorkflowHandler struct {
	log      logrus.FieldLogger
	baseURL  *url.URL
	authMode string
	apiToken string
	proxy    *httputil.ReverseProxy
}

// NewWorkflowHandler builds a workflow passthrough for the given config. It
// returns an error when the configured URL cannot be parsed.
func NewWorkflowHandler(log logrus.FieldLogger, cfg WorkflowConfig) (*WorkflowHandler, error) {
	base, err := url.Parse(strings.TrimSpace(cfg.URL))
	if err != nil {
		return nil, fmt.Errorf("parsing workflow url: %w", err)
	}

	// Reuse the shared handler transport but disable transparent gzip: Go's
	// Transport auto-adds Accept-Encoding: gzip and decompresses, which buffers
	// a block before emitting and defeats FlushInterval: -1 for SSE.
	transport := newProxyTransport(false)
	transport.DisableCompression = true

	mode := strings.TrimSpace(cfg.AuthMode)
	if mode == "" {
		mode = WorkflowAuthModeToken
	}

	h := &WorkflowHandler{
		log:      log.WithField("handler", "workflow"),
		baseURL:  base,
		authMode: mode,
		apiToken: cfg.APIToken,
	}

	h.proxy = &httputil.ReverseProxy{
		Transport: transport,
		// Flush every write so SSE frames reach the client immediately.
		FlushInterval: -1,
		Rewrite:       h.rewrite,
		ErrorHandler:  h.errorHandler,
	}

	return h, nil
}

// rewrite rebuilds the outbound request: upstream target, explicit path, header
// allow-list, and Authorization set per auth mode.
func (h *WorkflowHandler) rewrite(pr *httputil.ProxyRequest) {
	pr.SetURL(h.baseURL)

	// SetURL joins the target path with the inbound path, which would leak the
	// /workflow mount prefix upstream. Overwrite Path and RawPath explicitly
	// with the computed /api/v1/<rest> (this is why url must be path-less).
	data, _ := pr.In.Context().Value(workflowDataKey).(*workflowReqData)
	if data != nil {
		pr.Out.URL.Path = data.outPath
		pr.Out.URL.RawPath = data.rawPath
	}

	pr.Out.Host = pr.Out.URL.Host

	// Replace the (hop-by-hop-stripped) inbound headers with the shared strict
	// allow-list. This drops Authorization, Cookie, Host, and the attribution
	// header by construction before we set our own Authorization.
	allowed := workflowrelay.FilterHeaders(pr.In.Header)

	switch h.authMode {
	case WorkflowAuthModePassthrough:
		// Re-attach the caller's own verified bearer unchanged; the engine
		// validates it directly.
		if inbound := pr.In.Header.Get("Authorization"); inbound != "" {
			allowed.Set("Authorization", inbound)
		}
	default:
		// token mode: inject the proxy-held api_token; the inbound proxy bearer
		// never reaches upstream.
		if h.apiToken != "" {
			allowed.Set("Authorization", "Bearer "+h.apiToken)
		}
	}

	pr.Out.Header = allowed

	pr.SetXForwarded()

	// The allow-list already drops attribution; delete it explicitly so a
	// future addition to ForwardedHeaders cannot leak it upstream.
	pr.Out.Header.Del(attribution.Header)
}

// errorHandler returns a clean 502 problem+json when the upstream is
// unreachable.
func (h *WorkflowHandler) errorHandler(w http.ResponseWriter, _ *http.Request, err error) {
	h.log.WithError(err).Warn("workflow upstream error")
	workflowrelay.WriteProblem(w, http.StatusBadGateway, "Bad Gateway",
		"workflow engine is configured but unreachable")
}

// ServeHTTP resolves and clamps the request path, then proxies the request. It
// clears the response write deadline so SSE streams outlive server.write_timeout.
func (h *WorkflowHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Clear the write deadline (best-effort) so a long-lived SSE stream is not
	// cut off by server.write_timeout. Wrapping writers expose Unwrap() so the
	// controller reaches the real writer.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})

	outPath, rawPath, err := workflowCleanPath(chi.URLParam(r, "*"))
	if err != nil {
		workflowrelay.WriteProblem(w, http.StatusBadRequest, "Bad Request", err.Error())

		return
	}

	ctx := context.WithValue(r.Context(), workflowDataKey, &workflowReqData{
		outPath: outPath,
		rawPath: rawPath,
	})

	h.proxy.ServeHTTP(w, r.WithContext(ctx))
}

// workflowCleanPath clamps and cleans the wildcard remainder into the upstream
// path pair (decoded Path, encoded RawPath) rooted at /api/v1. It rejects path
// traversal and strips a redundant leading /api/v1 segment from the raw form so
// the prefix is never doubled.
//
// chi hands us the raw (still percent-encoded) wildcard for most inputs, but a
// double-encoded input (e.g. %252e%252e) arrives already decoded once. We decode
// exactly once here to obtain a single canonical representation for ALL traversal
// checks, so both single- (%2e%2e) and double-encoded (%252e%252e) traversal —
// plus matrix-param (`..;`) and backslash (`..\..`) variants — surface as literal
// "..". The raw form is what we path.Clean and forward, so percent-encoded
// reserved characters in a segment (steer keys like tasks.loop.child%5Biter%3D0002%5D,
// or an encoded slash %2F) survive intact instead of being decoded and re-split.
func workflowCleanPath(rest string) (outPath, rawPath string, err error) {
	decoded, decErr := url.PathUnescape(rest)
	if decErr != nil {
		return "", "", errors.New("invalid path encoding")
	}

	if traversalErr := workflowrelay.RejectTraversal(decoded); traversalErr != nil {
		return "", "", traversalErr
	}

	// Clean the raw (still percent-encoded) remainder to collapse `.`/`..`
	// segments and duplicate slashes.
	cleanedRaw := path.Clean("/" + strings.TrimPrefix(rest, "/"))

	// Re-verify AFTER cleaning, on the decoded form, so path.Clean can never
	// hand a surviving ".." segment upstream (defence in depth over the pre-clean
	// check above).
	cleanedDecoded, decErr := url.PathUnescape(cleanedRaw)
	if decErr != nil {
		return "", "", errors.New("invalid path encoding")
	}

	if traversalErr := workflowrelay.RejectTraversal(cleanedDecoded); traversalErr != nil {
		return "", "", traversalErr
	}

	cleanedRaw = workflowTrimAPIV1Prefix(cleanedRaw)
	if cleanedRaw == "" || cleanedRaw == "/" {
		rawPath = "/api/v1"
	} else {
		if !strings.HasPrefix(cleanedRaw, "/") {
			cleanedRaw = "/" + cleanedRaw
		}

		rawPath = "/api/v1" + cleanedRaw
	}

	outPath, decErr = url.PathUnescape(rawPath)
	if decErr != nil {
		return "", "", errors.New("invalid path encoding")
	}

	return outPath, rawPath, nil
}

// workflowTrimAPIV1Prefix strips a leading "/api/v1" ONLY on a full-segment
// boundary (exact match or followed by "/"), so "/api/v1foo" is never mangled
// into "/foo". It returns "" for an exact "/api/v1" so the caller re-roots it.
func workflowTrimAPIV1Prefix(p string) string {
	const prefix = "/api/v1"

	switch {
	case p == prefix:
		return ""
	case strings.HasPrefix(p, prefix+"/"):
		return strings.TrimPrefix(p, prefix)
	default:
		return p
	}
}
