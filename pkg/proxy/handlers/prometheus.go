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

// PrometheusConfig holds Prometheus proxy configuration for a single instance.
type PrometheusConfig struct {
	Name        string
	Description string
	URL         string
	Username    string
	Password    string
	SkipVerify  bool
	Timeout     int
}

// PrometheusHandler handles requests to Prometheus instances.
type PrometheusHandler struct {
	log       logrus.FieldLogger
	instances map[string]*prometheusInstance
}

type prometheusInstance struct {
	cfg   PrometheusConfig
	proxy *httputil.ReverseProxy
}

// NewPrometheusHandler creates a new Prometheus handler.
func NewPrometheusHandler(log logrus.FieldLogger, configs []PrometheusConfig) *PrometheusHandler {
	h := &PrometheusHandler{
		log:       log.WithField("handler", "prometheus"),
		instances: make(map[string]*prometheusInstance, len(configs)),
	}

	for _, cfg := range configs {
		h.instances[cfg.Name] = h.createInstance(cfg)
	}

	return h
}

func (h *PrometheusHandler) createInstance(cfg PrometheusConfig) *prometheusInstance {
	targetURL, err := url.Parse(cfg.URL)
	if err != nil {
		h.log.WithError(err).WithField("instance", cfg.Name).Error("Failed to parse URL")

		return nil
	}

	// Create reverse proxy.
	rp := httputil.NewSingleHostReverseProxy(targetURL)

	rp.Transport = newProxyTransport(cfg.SkipVerify)

	// Customize the director to add auth.
	originalDirector := rp.Director
	rp.Director = func(req *http.Request) {
		originalDirector(req)

		// Remove the sandbox's Authorization header (Bearer token) before adding our own.
		req.Header.Del("Authorization")

		// Add basic auth if configured.
		if cfg.Username != "" {
			req.SetBasicAuth(cfg.Username, cfg.Password)
		}

		// Set req.Host to the target host. The default director only sets req.URL.Host,
		// but Go's http.Client uses req.Host for the Host header when sending requests.
		req.Host = req.URL.Host

		// Also delete any existing Host header to avoid conflicts.
		req.Header.Del("Host")
	}

	// Error handler.
	rp.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		h.log.WithError(err).WithField("instance", cfg.Name).Error("Proxy error")
		http.Error(w, fmt.Sprintf("proxy error: %v", err), http.StatusBadGateway)
	}

	return &prometheusInstance{
		cfg:   cfg,
		proxy: rp,
	}
}

// ServeHTTP handles Prometheus requests. The instance is specified via X-Datasource header.
func (h *PrometheusHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Extract instance name from header.
	instanceName := r.Header.Get(DatasourceHeader)
	if instanceName == "" {
		http.Error(w, fmt.Sprintf("missing %s header", DatasourceHeader), http.StatusBadRequest)

		return
	}

	instance, ok := h.instances[instanceName]
	if !ok {
		http.Error(w, fmt.Sprintf("unknown instance: %s", instanceName), http.StatusNotFound)

		return
	}

	if instance == nil {
		http.Error(w, fmt.Sprintf("instance %s not properly configured", instanceName), http.StatusInternalServerError)

		return
	}

	// Strip /prometheus prefix from path, keep the rest for the upstream.
	path := strings.TrimPrefix(r.URL.Path, "/prometheus")
	if path == "" {
		path = "/"
	}

	r.URL.Path = path

	if instance.cfg.Timeout > 0 {
		timeoutCtx, cancel := context.WithTimeout(r.Context(), time.Duration(instance.cfg.Timeout)*time.Second)
		defer cancel()

		r = r.WithContext(timeoutCtx)
	}

	h.log.WithFields(logrus.Fields{
		"instance": instanceName,
		"path":     path,
		"method":   r.Method,
	}).Debug("Proxying Prometheus request")

	instance.proxy.ServeHTTP(w, r)
}

// Instances returns the list of configured instance names.
func (h *PrometheusHandler) Instances() []string {
	names := make([]string, 0, len(h.instances))
	for name := range h.instances {
		names = append(names, name)
	}

	return names
}
