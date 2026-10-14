package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/sirupsen/logrus"
)

// RolloorHandler forwards /rolloor/{network}/api/v1/... to that network's
// rolloor with the caller's own bearer token, which rolloor verifies and
// authorises itself. It holds no credential and needs no configuration.
type RolloorHandler struct {
	log logrus.FieldLogger
	// upstream is the rolloor for a network; replaceable in tests.
	upstream func(network string) *url.URL
	proxy    *httputil.ReverseProxy
}

// NewRolloorHandler creates the handler.
func NewRolloorHandler(log logrus.FieldLogger) *RolloorHandler {
	h := &RolloorHandler{
		log: log.WithField("handler", "rolloor"),
		upstream: func(network string) *url.URL {
			return &url.URL{Scheme: "https", Host: fmt.Sprintf("rolloor.%s.ethpandaops.io", network)}
		},
	}

	h.proxy = &httputil.ReverseProxy{
		Transport: newProxyTransport(false),
		Rewrite:   h.rewrite,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			h.log.WithError(err).Warn("rolloor upstream error")
			http.Error(w, "rolloor is unreachable", http.StatusBadGateway)
		},
	}

	return h
}

type rolloorTarget struct {
	upstream *url.URL
	path     string
}

type rolloorTargetKey struct{}

// ServeHTTP checks the network and the path, then proxies.
func (h *RolloorHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/rolloor/")
	network, path, _ := strings.Cut(rest, "/")

	if !validSegment.MatchString(network) {
		http.Error(w, "invalid network name: must match [a-z0-9-]", http.StatusBadRequest)

		return
	}

	if !strings.HasPrefix(path, "api/v1/") || strings.Contains(path, "..") {
		http.Error(w, "only /api/v1/ paths are forwarded", http.StatusBadRequest)

		return
	}

	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)

		return
	}

	h.log.WithFields(logrus.Fields{"network": network, "path": path, "method": r.Method}).Debug("Proxying rolloor request")

	target := &rolloorTarget{upstream: h.upstream(network), path: "/" + path}
	h.proxy.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), rolloorTargetKey{}, target)))
}

// rewrite sends only the caller's bearer, the body's type and what it
// accepts; nothing else from the inbound request reaches rolloor.
func (h *RolloorHandler) rewrite(pr *httputil.ProxyRequest) {
	target, _ := pr.In.Context().Value(rolloorTargetKey{}).(*rolloorTarget)

	pr.SetURL(target.upstream)
	pr.Out.URL.Path = target.path
	pr.Out.URL.RawPath = ""
	pr.Out.Host = target.upstream.Host

	out := http.Header{}
	for _, k := range []string{"Authorization", "Content-Type", "Accept"} {
		if v := pr.In.Header.Get(k); v != "" {
			out.Set(k, v)
		}
	}

	pr.Out.Header = out
}
