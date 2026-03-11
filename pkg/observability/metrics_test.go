package observability

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestPackageImportDoesNotRegisterPandaMetrics(t *testing.T) {
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather default metrics: %v", err)
	}

	for _, family := range families {
		if strings.HasPrefix(family.GetName(), metricsNamespace+"_") {
			t.Fatalf("unexpected panda metric registered on default registry: %s", family.GetName())
		}
	}
}

func TestRegisterMetricsRegistersCollectors(t *testing.T) {
	reg := prometheus.NewRegistry()
	if err := RegisterMetrics(reg); err != nil {
		t.Fatalf("RegisterMetrics failed: %v", err)
	}

	ToolCallsTotal.WithLabelValues("search", "success").Inc()
	ToolCallDuration.WithLabelValues("search").Observe(0.25)
	ActiveConnections.Inc()
	defer ActiveConnections.Dec()

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather registered metrics: %v", err)
	}

	names := make(map[string]struct{}, len(families))
	for _, family := range families {
		names[family.GetName()] = struct{}{}
	}

	for _, metricName := range []string{
		"panda_tool_calls_total",
		"panda_tool_call_duration_seconds",
		"panda_active_connections",
	} {
		if _, ok := names[metricName]; !ok {
			t.Fatalf("expected metric %s to be registered", metricName)
		}
	}
}
