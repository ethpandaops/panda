// Package handlers provides reverse proxy handlers for each datasource type.
package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

// ClickHouseDatasourceConfig holds ClickHouse proxy configuration for a single datasource.
type ClickHouseDatasourceConfig struct {
	Name        string
	Description string
	Host        string
	Port        int
	Database    string
	Username    string
	Password    string
	Secure      bool
	SkipVerify  bool
	Timeout     int
}

// ClickHouseConfig is kept as an alias while callers migrate to the datasource terminology.
type ClickHouseConfig = ClickHouseDatasourceConfig

// ClickHouseHandler handles requests to ClickHouse datasources.
type ClickHouseHandler struct {
	log         logrus.FieldLogger
	datasources map[string]*clickhouseDatasource
}

type clickhouseDatasource struct {
	cfg   ClickHouseDatasourceConfig
	proxy *httputil.ReverseProxy
}

// NewClickHouseHandler creates a new ClickHouse handler.
func NewClickHouseHandler(log logrus.FieldLogger, configs []ClickHouseDatasourceConfig) *ClickHouseHandler {
	h := &ClickHouseHandler{
		log:         log.WithField("handler", "clickhouse"),
		datasources: make(map[string]*clickhouseDatasource, len(configs)),
	}

	for _, cfg := range configs {
		h.datasources[cfg.Name] = h.createDatasource(cfg)
	}

	return h
}

func (h *ClickHouseHandler) createDatasource(cfg ClickHouseDatasourceConfig) *clickhouseDatasource {
	// Build target URL.
	scheme := "https"
	if !cfg.Secure {
		scheme = "http"
	}

	targetURL := &url.URL{
		Scheme: scheme,
		Host:   fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
	}

	// Create reverse proxy.
	rp := httputil.NewSingleHostReverseProxy(targetURL)

	rp.Transport = newProxyTransport(cfg.SkipVerify)

	// Customize the director to add auth and database.
	originalDirector := rp.Director
	rp.Director = func(req *http.Request) {
		originalDirector(req)

		// Remove the sandbox's Authorization header (Bearer token) before adding our own.
		req.Header.Del("Authorization")

		// Add basic auth for ClickHouse.
		if cfg.Username != "" {
			req.SetBasicAuth(cfg.Username, cfg.Password)
		}

		// Add default database as query param if not already set.
		q := req.URL.Query()
		if q.Get("database") == "" && cfg.Database != "" {
			q.Set("database", cfg.Database)
		}

		req.URL.RawQuery = q.Encode()

		// Set req.Host to the target host. The default director only sets req.URL.Host,
		// but Go's http.Client uses req.Host for the Host header when sending requests.
		// Without this, Cloudflare rejects requests with mismatched Host headers.
		req.Host = req.URL.Host

		// Also delete any existing Host header to avoid conflicts.
		req.Header.Del("Host")
	}

	// Error handler.
	rp.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		h.log.WithError(err).WithField("datasource", cfg.Name).Error("Proxy error")
		http.Error(w, fmt.Sprintf("proxy error: %v", err), http.StatusBadGateway)
	}

	return &clickhouseDatasource{
		cfg:   cfg,
		proxy: rp,
	}
}

// ServeHTTP handles ClickHouse requests. The datasource is specified via X-Datasource header.
func (h *ClickHouseHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Extract datasource name from header.
	datasourceName := r.Header.Get(DatasourceHeader)
	if datasourceName == "" {
		http.Error(w, fmt.Sprintf("missing %s header", DatasourceHeader), http.StatusBadRequest)

		return
	}

	datasource, ok := h.datasources[datasourceName]
	if !ok {
		http.Error(w, fmt.Sprintf("unknown datasource: %s", datasourceName), http.StatusNotFound)

		return
	}

	// Strip /clickhouse prefix from path, keep the rest for the upstream.
	path := strings.TrimPrefix(r.URL.Path, "/clickhouse")
	if path == "" {
		path = "/"
	}

	r.URL.Path = path

	if datasource.cfg.Timeout > 0 {
		timeoutCtx, cancel := context.WithTimeout(r.Context(), time.Duration(datasource.cfg.Timeout)*time.Second)
		defer cancel()

		r = r.WithContext(timeoutCtx)
	}

	h.log.WithFields(logrus.Fields{
		"datasource": datasourceName,
		"path":       path,
		"method":     r.Method,
	}).Debug("Proxying ClickHouse request")

	datasource.proxy.ServeHTTP(w, r)
}

// Datasources returns the list of configured datasource names.
func (h *ClickHouseHandler) Datasources() []string {
	names := make([]string, 0, len(h.datasources))
	for name := range h.datasources {
		names = append(names, name)
	}

	return names
}

// Clusters is kept as a compatibility wrapper while callers migrate.
func (h *ClickHouseHandler) Clusters() []string {
	return h.Datasources()
}
