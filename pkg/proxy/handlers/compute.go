package handlers

import (
	"context"
	"net/http/httputil"
	"net/url"

	"github.com/sirupsen/logrus"
)

// DefaultForwardedSubjectHeader is the header the compute backend reads to
// learn the end-user identity asserted by this trusted proxy.
const DefaultForwardedSubjectHeader = "X-Authentik-Sub"

// ComputeConfig holds compute proxy configuration for a single datasource.
type ComputeConfig struct {
	Name        string
	Description string
	URL         string
	// Token is the shared service bearer token the compute backend uses to
	// authenticate this proxy as a trusted caller.
	Token string
	// ForwardedSubjectHeader is the header used to forward the verified
	// end-user subject upstream. Defaults to DefaultForwardedSubjectHeader.
	ForwardedSubjectHeader string
}

// ComputeHandler handles requests to compute API datasources. The datasource
// is specified via the X-Datasource header. Unlike read-only datasources, all
// HTTP methods are forwarded: the compute backend manages mutable resources
// (sandboxes, snapshots) and authorizes each request itself using the
// forwarded subject.
type ComputeHandler struct {
	*datasourceHandler
	subjectFn func(context.Context) string
}

// NewComputeHandler creates a new compute handler. subjectFn extracts the
// verified end-user subject from a request context; the handler forwards it
// upstream so the compute backend can map it to a user. It must derive from
// the proxy-verified identity, never from an inbound header.
func NewComputeHandler(log logrus.FieldLogger, configs []ComputeConfig, subjectFn func(context.Context) string) *ComputeHandler {
	h := &ComputeHandler{
		datasourceHandler: newDatasourceHandler(log, "compute", "/compute"),
		subjectFn:         subjectFn,
	}

	for _, cfg := range configs {
		h.datasources[cfg.Name] = h.createDatasource(cfg)
	}

	return h
}

func (h *ComputeHandler) createDatasource(cfg ComputeConfig) *datasourceProxy {
	targetURL, err := url.Parse(cfg.URL)
	if err != nil {
		h.log.WithError(err).WithField("datasource", cfg.Name).Error("Failed to parse URL")

		return nil
	}

	subjectHeader := cfg.ForwardedSubjectHeader
	if subjectHeader == "" {
		subjectHeader = DefaultForwardedSubjectHeader
	}

	rp := &httputil.ReverseProxy{Transport: newProxyTransport(false)}

	rp.Rewrite = func(pr *httputil.ProxyRequest) {
		pr.SetURL(targetURL)
		pr.SetXForwarded()

		// Strip the caller's credentials before asserting our own. The compute
		// backend authenticates this proxy via the shared service token, then
		// trusts the forwarded subject for the user identity.
		pr.Out.Header.Del("Authorization")
		pr.Out.Header.Del("Cookie")

		// Never trust an inbound subject header; the backend honours it only
		// when authenticated by the service token, so a spoofed value would
		// otherwise impersonate a user.
		pr.Out.Header.Del(subjectHeader)

		if cfg.Token != "" {
			pr.Out.Header.Set("Authorization", "Bearer "+cfg.Token)
		}

		if h.subjectFn != nil {
			if subject := h.subjectFn(pr.In.Context()); subject != "" {
				pr.Out.Header.Set(subjectHeader, subject)
			}
		}

		// Set the outbound Host to the target host. SetURL only sets URL.Host,
		// but Go's http.Client uses req.Host for the Host header when sending requests.
		pr.Out.Host = pr.Out.URL.Host
		pr.Out.Header.Del("Host")
	}

	rp.ErrorHandler = h.proxyErrorHandler(cfg.Name)

	return &datasourceProxy{proxy: rp}
}
