// Package handlers provides reverse proxy handlers for each datasource type.
package handlers

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

// DatasourceHeader is the HTTP header used to specify which datasource to route to.
const DatasourceHeader = "X-Datasource"

// ClickHouseConfig holds ClickHouse proxy configuration for a single cluster.
type ClickHouseConfig struct {
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

// ClickHouseHandler handles requests to ClickHouse clusters.
type ClickHouseHandler struct {
	log      logrus.FieldLogger
	clusters map[string]*clickhouseCluster
}

type clickhouseCluster struct {
	cfg   ClickHouseConfig
	proxy *httputil.ReverseProxy
}

// NewClickHouseHandler creates a new ClickHouse handler.
func NewClickHouseHandler(log logrus.FieldLogger, configs []ClickHouseConfig) *ClickHouseHandler {
	h := &ClickHouseHandler{
		log:      log.WithField("handler", "clickhouse"),
		clusters: make(map[string]*clickhouseCluster, len(configs)),
	}

	for _, cfg := range configs {
		h.clusters[cfg.Name] = h.createCluster(cfg)
	}

	return h
}

func (h *ClickHouseHandler) createCluster(cfg ClickHouseConfig) *clickhouseCluster {
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

	// Configure transport with TLS settings.
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: cfg.SkipVerify, //nolint:gosec // User-configured
		},
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	}
	rp.Transport = transport

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
		h.log.WithError(err).WithField("cluster", cfg.Name).Error("Proxy error")
		http.Error(w, fmt.Sprintf("proxy error: %v", err), http.StatusBadGateway)
	}

	return &clickhouseCluster{
		cfg:   cfg,
		proxy: rp,
	}
}

// ServeHTTP handles ClickHouse requests. The cluster is specified via X-Datasource header.
func (h *ClickHouseHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Extract cluster name from header.
	clusterName := r.Header.Get(DatasourceHeader)
	if clusterName == "" {
		http.Error(w, fmt.Sprintf("missing %s header", DatasourceHeader), http.StatusBadRequest)

		return
	}

	cluster, ok := h.clusters[clusterName]
	if !ok {
		http.Error(w, fmt.Sprintf("unknown cluster: %s", clusterName), http.StatusNotFound)

		return
	}

	// Strip /clickhouse prefix from path, keep the rest for the upstream.
	path := strings.TrimPrefix(r.URL.Path, "/clickhouse")
	if path == "" {
		path = "/"
	}

	r.URL.Path = path

	if cluster.cfg.Timeout > 0 {
		timeoutCtx, cancel := context.WithTimeout(r.Context(), time.Duration(cluster.cfg.Timeout)*time.Second)
		defer cancel()

		r = r.WithContext(timeoutCtx)
	}

	h.log.WithFields(logrus.Fields{
		"cluster": clusterName,
		"path":    path,
		"method":  r.Method,
	}).Debug("Proxying ClickHouse request")

	cluster.proxy.ServeHTTP(w, r)
}

// Clusters returns the list of configured cluster names.
func (h *ClickHouseHandler) Clusters() []string {
	names := make([]string, 0, len(h.clusters))
	for name := range h.clusters {
		names = append(names, name)
	}

	return names
}
