// Package handlers provides reverse proxy handlers for each datasource type.
package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// DatasourceHeader is the HTTP header used to specify which datasource to route to.
const DatasourceHeader = "X-Datasource"

type datasourceRouteContextKey struct{}

// WithDatasourceRoute stores the selected backend route for the current request.
func WithDatasourceRoute(ctx context.Context, routeName string) context.Context {
	return context.WithValue(ctx, datasourceRouteContextKey{}, routeName)
}

func datasourceRoute(r *http.Request, fallback string) string {
	routeName, _ := r.Context().Value(datasourceRouteContextKey{}).(string)
	if routeName == "" {
		return fallback
	}

	return routeName
}

// ClickHouseConfig holds ClickHouse proxy configuration for a single cluster.
type ClickHouseConfig struct {
	Name        string
	RouteName   string
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

// ClickHouseHandler handles requests to ClickHouse clusters. Clusters may be
// added or removed at runtime (e.g. by autodiscovery), so all access to the
// cluster map and name list is guarded by mu.
type ClickHouseHandler struct {
	log logrus.FieldLogger

	mu       sync.RWMutex
	clusters map[string]*clickhouseCluster
	names    []string
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
		h.names = appendUniqueName(h.names, cfg.Name)
		h.clusters[handlerRouteName(cfg.Name, cfg.RouteName)] = h.createCluster(cfg)
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
	rp := &httputil.ReverseProxy{Transport: newProxyTransport(cfg.SkipVerify)}

	rp.Rewrite = func(pr *httputil.ProxyRequest) {
		pr.SetURL(targetURL)
		pr.SetXForwarded()

		// Remove the sandbox's Authorization header (Bearer token) before adding our own.
		pr.Out.Header.Del("Authorization")

		// Add basic auth for ClickHouse.
		if cfg.Username != "" {
			pr.Out.SetBasicAuth(cfg.Username, cfg.Password)
		}

		// Add default database as query param if not already set.
		q := pr.Out.URL.Query()
		if q.Get("database") == "" && cfg.Database != "" {
			q.Set("database", cfg.Database)
		}

		pr.Out.URL.RawQuery = q.Encode()

		// Set the outbound Host to the target host. SetURL only sets URL.Host,
		// but Go's http.Client uses req.Host for the Host header when sending requests.
		// Without this, Cloudflare rejects requests with mismatched Host headers.
		pr.Out.Host = pr.Out.URL.Host

		// Also delete any existing Host header to avoid conflicts.
		pr.Out.Header.Del("Host")
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

	h.mu.RLock()
	cluster, ok := h.clusters[datasourceRoute(r, clusterName)]
	h.mu.RUnlock()

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
	h.mu.RLock()
	defer h.mu.RUnlock()

	return append([]string(nil), h.names...)
}

// ClusterConfig returns the current configuration for a cluster name.
func (h *ClickHouseHandler) ClusterConfig(name string) (ClickHouseConfig, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if cluster, ok := h.clusters[name]; ok {
		return cluster.cfg, true
	}

	for _, cluster := range h.clusters {
		if cluster.cfg.Name == name {
			return cluster.cfg, true
		}
	}

	return ClickHouseConfig{}, false
}

// AddCluster adds or replaces a ClickHouse cluster at runtime.
func (h *ClickHouseHandler) AddCluster(cfg ClickHouseConfig) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.names = appendUniqueName(h.names, cfg.Name)
	h.clusters[handlerRouteName(cfg.Name, cfg.RouteName)] = h.createCluster(cfg)
}

// RemoveCluster removes a ClickHouse cluster at runtime.
func (h *ClickHouseHandler) RemoveCluster(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	for routeName, cluster := range h.clusters {
		if cluster.cfg.Name == name {
			delete(h.clusters, routeName)
		}
	}

	h.names = removeName(h.names, name)
}

// HasCluster reports whether a cluster is currently configured.
func (h *ClickHouseHandler) HasCluster(name string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if _, ok := h.clusters[name]; ok {
		return true
	}

	for _, cluster := range h.clusters {
		if cluster.cfg.Name == name {
			return true
		}
	}

	return false
}

func handlerRouteName(name, routeName string) string {
	if routeName != "" {
		return routeName
	}

	return name
}

func appendUniqueName(names []string, name string) []string {
	for _, existing := range names {
		if existing == name {
			return names
		}
	}

	return append(names, name)
}

func removeName(names []string, name string) []string {
	for i, existing := range names {
		if existing == name {
			return append(names[:i], names[i+1:]...)
		}
	}

	return names
}
