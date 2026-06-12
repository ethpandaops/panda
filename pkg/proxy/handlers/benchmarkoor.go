package handlers

import (
	"net/http"
	"net/http/httputil"
	"net/url"

	"github.com/sirupsen/logrus"
)

// BenchmarkoorConfig holds benchmarkoor proxy configuration for a single datasource.
type BenchmarkoorConfig struct {
	Name        string
	Description string
	URL         string
	APIKey      string
}

// BenchmarkoorHandler handles requests to benchmarkoor API datasources.
// The datasource is specified via the X-Datasource header. Only read
// methods are forwarded: benchmarkoor API keys are read-only upstream, and
// the proxy enforces the same boundary before spending a request.
type BenchmarkoorHandler struct {
	*datasourceHandler
}

// NewBenchmarkoorHandler creates a new benchmarkoor handler.
func NewBenchmarkoorHandler(log logrus.FieldLogger, configs []BenchmarkoorConfig) *BenchmarkoorHandler {
	h := &BenchmarkoorHandler{
		datasourceHandler: newDatasourceHandler(log, "benchmarkoor", "/benchmarkoor"),
	}

	for _, cfg := range configs {
		h.datasources[cfg.Name] = h.createDatasource(cfg)
	}

	return h
}

// ServeHTTP rejects non-read methods before delegating to the generic
// datasource router.
func (h *BenchmarkoorHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "benchmarkoor proxy access is read-only (GET/HEAD)")

		return
	}

	h.datasourceHandler.ServeHTTP(w, r)
}

func (h *BenchmarkoorHandler) createDatasource(cfg BenchmarkoorConfig) *datasourceProxy {
	targetURL, err := url.Parse(cfg.URL)
	if err != nil {
		h.log.WithError(err).WithField("datasource", cfg.Name).Error("Failed to parse URL")

		return nil
	}

	rp := &httputil.ReverseProxy{Transport: newProxyTransport(false)}

	rp.Rewrite = func(pr *httputil.ProxyRequest) {
		pr.SetURL(targetURL)
		pr.SetXForwarded()

		// Remove the sandbox's Authorization header (Bearer token) before adding our own.
		pr.Out.Header.Del("Authorization")

		// Strip any caller cookies so a forwarded benchmarkoor session can
		// never substitute for the configured key.
		pr.Out.Header.Del("Cookie")

		if cfg.APIKey != "" {
			pr.Out.Header.Set("Authorization", "Bearer "+cfg.APIKey)
		}

		// Set the outbound Host to the target host. SetURL only sets URL.Host,
		// but Go's http.Client uses req.Host for the Host header when sending requests.
		pr.Out.Host = pr.Out.URL.Host
		pr.Out.Header.Del("Host")
	}

	rp.ErrorHandler = h.proxyErrorHandler(cfg.Name)

	return &datasourceProxy{proxy: rp}
}
