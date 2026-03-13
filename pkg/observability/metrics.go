// Package observability provides metrics capabilities for ethpandaops-panda.
package observability

import (
	"fmt"

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

func RegisterMetrics(reg prometheus.Registerer) error {
	for _, collector := range []prometheus.Collector{
		ToolCallsTotal,
		ToolCallDuration,
		ActiveConnections,
	} {
		if err := reg.Register(collector); err != nil {
			return fmt.Errorf("registering metrics: %w", err)
		}
	}

	return nil
}
