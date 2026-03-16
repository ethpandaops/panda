package proxy

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/ethpandaops/panda/pkg/proxy/handlers"
)

const (
	proxyMetricsNamespace = "panda"
	proxyMetricsSubsystem = "proxy"
)

// Request metrics.
var (
	// ProxyRequestsTotal counts proxy requests by datasource, method, and status.
	ProxyRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: proxyMetricsNamespace,
			Subsystem: proxyMetricsSubsystem,
			Name:      "requests_total",
			Help:      "Total number of proxy requests",
		},
		[]string{"datasource_type", "datasource", "method", "status_code"},
	)

	// ProxyRequestDurationSeconds measures proxy request duration.
	ProxyRequestDurationSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: proxyMetricsNamespace,
			Subsystem: proxyMetricsSubsystem,
			Name:      "request_duration_seconds",
			Help:      "Duration of proxy requests in seconds",
			Buckets:   []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300},
		},
		[]string{"datasource_type", "datasource", "method"},
	)

	// ProxyResponseSizeBytes measures proxy response sizes.
	ProxyResponseSizeBytes = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: proxyMetricsNamespace,
			Subsystem: proxyMetricsSubsystem,
			Name:      "response_size_bytes",
			Help:      "Size of proxy responses in bytes",
			Buckets:   prometheus.ExponentialBuckets(100, 10, 8),
		},
		[]string{"datasource_type"},
	)

	// ProxyActiveRequests tracks currently in-flight requests.
	ProxyActiveRequests = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: proxyMetricsNamespace,
			Subsystem: proxyMetricsSubsystem,
			Name:      "active_requests",
			Help:      "Number of currently active proxy requests",
		},
		[]string{"datasource_type"},
	)

	// ProxyRateLimitRejectionsTotal counts rate limit rejections.
	ProxyRateLimitRejectionsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: proxyMetricsNamespace,
			Subsystem: proxyMetricsSubsystem,
			Name:      "rate_limit_rejections_total",
			Help:      "Total number of rate limit rejections",
		},
		[]string{"datasource_type"},
	)
)

func init() {
	prometheus.MustRegister(
		ProxyRequestsTotal,
		ProxyRequestDurationSeconds,
		ProxyResponseSizeBytes,
		ProxyActiveRequests,
		ProxyRateLimitRejectionsTotal,
	)
}

// metricsMiddleware returns an HTTP middleware that records request metrics.
// It runs outside the auth/rate-limit chain so rate-limited requests (429s) are counted.
func (s *server) metricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		dsType := extractDatasourceType(r.URL.Path)
		ds := s.metricsDatasourceLabel(dsType, r.Header.Get(handlers.DatasourceHeader))

		method := r.Method

		activeGauge := ProxyActiveRequests.WithLabelValues(dsType)
		activeGauge.Inc()
		defer activeGauge.Dec()

		mrw := &responseCapture{
			ResponseWriter: w,
			statusCode:     http.StatusOK,
		}

		next.ServeHTTP(mrw, r)

		duration := time.Since(start).Seconds()
		statusCode := strconv.Itoa(mrw.statusCode)

		ProxyRequestsTotal.WithLabelValues(
			dsType, ds, method, statusCode,
		).Inc()
		ProxyRequestDurationSeconds.WithLabelValues(
			dsType, ds, method,
		).Observe(duration)
		ProxyResponseSizeBytes.WithLabelValues(
			dsType,
		).Observe(float64(mrw.bytesWritten))
	})
}

func (s *server) metricsDatasourceLabel(dsType, candidate string) string {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return "default"
	}

	switch dsType {
	case "clickhouse":
		for _, cfg := range s.cfg.ClickHouse {
			if cfg.Name == candidate {
				return candidate
			}
		}
	case "prometheus":
		for _, cfg := range s.cfg.Prometheus {
			if cfg.Name == candidate {
				return candidate
			}
		}
	case "loki":
		for _, cfg := range s.cfg.Loki {
			if cfg.Name == candidate {
				return candidate
			}
		}
	}

	return "unknown"
}

// responseCapture wraps http.ResponseWriter to capture status code and bytes written.
// Used by both the metrics middleware and the auditor.
type responseCapture struct {
	http.ResponseWriter
	statusCode   int
	bytesWritten int
}

// WriteHeader captures the status code.
func (w *responseCapture) WriteHeader(code int) {
	w.statusCode = code
	w.ResponseWriter.WriteHeader(code)
}

// Write captures the number of bytes written.
func (w *responseCapture) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	w.bytesWritten += n

	return n, err
}

// Flush implements http.Flusher for streaming response support via reverse proxies.
func (w *responseCapture) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// extractDatasourceType extracts the datasource type from the request path.
func extractDatasourceType(path string) string {
	trimmed := strings.TrimPrefix(path, "/")

	if idx := strings.IndexByte(trimmed, '/'); idx > 0 {
		trimmed = trimmed[:idx]
	}

	switch trimmed {
	case "clickhouse":
		return "clickhouse"
	case "prometheus":
		return "prometheus"
	case "loki":
		return "loki"
	case "beacon", "execution":
		return "ethnode"
	case "datasources":
		return "datasources"
	default:
		return "unknown"
	}
}
