package resource

import (
	"io"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/types"
)

type captureEmbedder struct {
	texts []string
}

func (e *captureEmbedder) Embed(text string) ([]float32, error) {
	e.texts = append(e.texts, text)

	return []float32{1}, nil
}

func (e *captureEmbedder) EmbedBatch(texts []string) ([][]float32, error) {
	e.texts = append(e.texts, texts...)

	vectors := make([][]float32, len(texts))
	for i := range vectors {
		vectors[i] = []float32{1}
	}

	return vectors, nil
}

func (e *captureEmbedder) Close() error { return nil }

func TestExtractTableNames(t *testing.T) {
	tests := []struct {
		name     string
		query    string
		expected []string
	}{
		{
			name:     "clickhouse-refined with network prefix",
			query:    "SELECT * FROM {network}.fct_block_head FINAL WHERE slot > 100",
			expected: []string{"fct_block_head"},
		},
		{
			name:     "clickhouse-raw with bare table name",
			query:    "SELECT * FROM canonical_beacon_validators WHERE meta_network_name = 'mainnet'",
			expected: []string{"canonical_beacon_validators"},
		},
		{
			name:     "multiple tables with JOIN",
			query:    "SELECT * FROM {network}.fct_block b FINAL JOIN {network}.fct_block_first_seen_by_node t ON b.slot = t.slot",
			expected: []string{"fct_block", "fct_block_first_seen_by_node"},
		},
		{
			name:     "default schema prefix",
			query:    "SELECT * FROM default.beacon_api_eth_v1_events_attestation",
			expected: []string{"beacon_api_eth_v1_events_attestation"},
		},
		{
			name:     "CTE aliases not included",
			query:    "WITH latest AS (SELECT max(epoch) FROM canonical_beacon_validators) SELECT * FROM canonical_beacon_validators",
			expected: []string{"canonical_beacon_validators"},
		},
		{
			name:     "no tables",
			query:    "SELECT 1",
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractTableNames(tt.query)
			if len(got) != len(tt.expected) {
				t.Errorf("extractTableNames() = %v, want %v", got, tt.expected)
				return
			}

			for i := range got {
				if got[i] != tt.expected[i] {
					t.Errorf("extractTableNames()[%d] = %q, want %q", i, got[i], tt.expected[i])
				}
			}
		})
	}
}

func TestExampleIndexEmbeddingTextUsesTargetLabel(t *testing.T) {
	embedder := &captureEmbedder{}
	log := logrus.New()
	log.SetOutput(io.Discard)

	_, err := NewExampleIndex(log, embedder, map[string]types.ExampleCategory{
		"prometheus": {
			Name: "Prometheus",
			Examples: []types.Example{{
				Name:        "Active targets",
				Description: "Find active scrape targets.",
				Query:       "up",
				Target:      "prometheus",
			}},
		},
	})
	require.NoError(t, err)
	require.Len(t, embedder.texts, 1)
	assert.Contains(t, embedder.texts[0], "Target: prometheus")
	assert.NotContains(t, embedder.texts[0], "Cluster:")
}
