package handlers

import (
	"net/http/httputil"
	"net/url"

	"github.com/sirupsen/logrus"
)

// ComputeConfig holds compute proxy configuration for a single datasource.
type ComputeConfig struct {
	Name        string
	Description string
	URL         string
}

// ComputeHandler handles requests to compute API datasources. The datasource
// is specified via the X-Datasource header. Unlike read-only datasources, all
// HTTP methods are forwarded: the compute backend manages mutable resources
// (sandboxes, images) and authorizes each request itself.
//
// The proxy has already verified the caller's OIDC bearer token (and gated
// access via allowed_orgs); the handler forwards that same token to the compute
// backend, which validates it directly and derives the end-user identity from
// it. There is no shared service token and no forwarded-subject header.
type ComputeHandler struct {
	*datasourceHandler
}

// NewComputeHandler creates a new compute handler.
func NewComputeHandler(log logrus.FieldLogger, configs []ComputeConfig) *ComputeHandler {
	h := &ComputeHandler{
		datasourceHandler: newDatasourceHandler(log, "compute", "/compute"),
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

	rp := &httputil.ReverseProxy{Transport: newProxyTransport(false)}

	rp.Rewrite = func(pr *httputil.ProxyRequest) {
		pr.SetURL(targetURL)
		pr.SetXForwarded()

		// Forward the caller's verified OIDC bearer token to the compute backend
		// unchanged; the backend validates it and derives the user from it. Drop
		// only cookies so a browser session can never substitute for the token.
		pr.Out.Header.Del("Cookie")

		// Set the outbound Host to the target host. SetURL only sets URL.Host,
		// but Go's http.Client uses req.Host for the Host header when sending requests.
		pr.Out.Host = pr.Out.URL.Host
		pr.Out.Header.Del("Host")
	}

	rp.ErrorHandler = h.proxyErrorHandler(cfg.Name)

	return &datasourceProxy{proxy: rp}
}
