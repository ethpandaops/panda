package observability

import (
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics namespace for all ethpandaops-panda metrics.
const metricsNamespace = "panda"

// Tool call metrics.
var (
	// ToolCallsTotal counts the total number of tool calls by tool name and status.
	ToolCallsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Name:      "tool_calls_total",
			Help:      "Total number of tool calls",
		},
		[]string{"tool", "status"},
	)

	// ToolCallDuration measures the duration of tool calls in seconds.
	ToolCallDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: metricsNamespace,
			Name:      "tool_call_duration_seconds",
			Help:      "Duration of tool calls in seconds",
			Buckets:   prometheus.ExponentialBuckets(0.1, 2, 10),
		},
		[]string{"tool"},
	)
)

// Connection metrics.
var (
	// ActiveConnections tracks the number of active MCP connections.
	ActiveConnections = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: metricsNamespace,
			Name:      "active_connections",
			Help:      "Number of active MCP connections",
		},
	)
)

// Workflow passthrough metrics. The /api/v1/workflow/* passthrough has no
// metrics middleware, so its handler records these itself after each request
// (once on stream completion for long-lived SSE).
var (
	// WorkflowPassthroughTotal counts workflow passthrough requests by method and
	// response status class (2xx, 4xx, 5xx, ...).
	WorkflowPassthroughTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Name:      "workflow_passthrough_total",
			Help:      "Total number of workflow passthrough requests",
		},
		[]string{"method", "status_class"},
	)

	// WorkflowPassthroughDuration measures workflow passthrough request duration
	// in seconds. It is recorded only for non-streaming responses: SSE streams
	// (text/event-stream) are excluded because the handler blocks for the whole
	// stream lifetime, so the elapsed time is the stream duration, not latency.
	WorkflowPassthroughDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: metricsNamespace,
			Name:      "workflow_passthrough_duration_seconds",
			Help:      "Duration of workflow passthrough requests in seconds",
			Buckets:   prometheus.ExponentialBuckets(0.05, 2, 12),
		},
		[]string{"method"},
	)
)

func init() {
	// Register all metrics with the default registry.
	prometheus.MustRegister(
		ToolCallsTotal,
		ToolCallDuration,
		ActiveConnections,
		WorkflowPassthroughTotal,
		WorkflowPassthroughDuration,
	)
}
