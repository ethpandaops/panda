package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/operations"
	"github.com/ethpandaops/panda/pkg/serverapi"
)

func TestParseClickHouseTSV(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantColumns []string
		wantRows    [][]string
		wantErr     bool
	}{
		{
			name:        "empty input",
			input:       "",
			wantColumns: nil,
			wantRows:    nil,
		},
		{
			name:        "whitespace only",
			input:       "   \n\t ",
			wantColumns: nil,
			wantRows:    nil,
		},
		{
			name:        "header only",
			input:       "count\tname",
			wantColumns: []string{"count", "name"},
			wantRows:    [][]string{},
		},
		{
			name:        "header and rows",
			input:       "count\tname\n10\talice\n20\tbob",
			wantColumns: []string{"count", "name"},
			wantRows:    [][]string{{"10", "alice"}, {"20", "bob"}},
		},
		{
			name:        "ragged rows are accepted",
			input:       "a\tb\tc\n1\t2\n3\t4\t5\t6",
			wantColumns: []string{"a", "b", "c"},
			wantRows:    [][]string{{"1", "2"}, {"3", "4", "5", "6"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			columns, rows, err := parseClickHouseTSV([]byte(tt.input))
			if tt.wantErr {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantColumns, columns)
			assert.Equal(t, tt.wantRows, rows)
		})
	}
}

func TestFormatLabelSet(t *testing.T) {
	tests := []struct {
		name        string
		labels      map[string]string
		quoteValues bool
		want        string
	}{
		{
			name:   "empty",
			labels: map[string]string{},
			want:   "{}",
		},
		{
			name:        "sorted bare values",
			labels:      map[string]string{"b": "2", "a": "1"},
			quoteValues: false,
			want:        "{a=1, b=2}",
		},
		{
			name:        "sorted quoted values",
			labels:      map[string]string{"job": "node", "app": "beacon"},
			quoteValues: true,
			want:        `{app="beacon", job="node"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, formatLabelSet(tt.labels, tt.quoteValues))
		})
	}
}

func TestPrintExampleResultsUsesNeutralTargetLabel(t *testing.T) {
	output := captureStdout(t, func() {
		printExampleResults([]*serverapi.SearchExampleResult{{
			CategoryName:    "Prometheus",
			ExampleName:     "Validator duty metrics",
			Description:     "Inspect validator duty latency.",
			Query:           "up",
			Target:          "prometheus",
			Dataset:         "metrics",
			SimilarityScore: 0.8,
		}})
	})

	assert.Contains(t, output, "  Target: prometheus")
	assert.Contains(t, output, "  Dataset: metrics")
	assert.NotContains(t, output, "Cluster:")
}

func TestPrintExampleUsageHintsAreGeneric(t *testing.T) {
	output := captureStdout(t, func() {
		printExampleUsageHints([]*serverapi.SearchExampleResult{{
			Query:   "SELECT count() FROM {network}.example",
			Target:  "warehouse",
			Dataset: "example-pack",
		}})
	})

	assert.Contains(t, output, "Search examples are reusable patterns")
	assert.Contains(t, output, "panda clickhouse query-raw <Target>")
	assert.Contains(t, output, "panda datasets <Dataset>")
	assert.NotContains(t, output, "mainnet")
	assert.NotContains(t, output, "slot")
}

func TestPrintExampleUsageHintsMentionMultipleTargets(t *testing.T) {
	output := captureStdout(t, func() {
		printExampleUsageHints([]*serverapi.SearchExampleResult{
			{Query: "SELECT 1", Target: "warehouse-a"},
			{Query: "SELECT 2", Target: "warehouse-b"},
		})
	})

	assert.Contains(t, output, "Results span multiple Targets")
	assert.Contains(t, output, "combine bounded results")
}

func TestPrintDatasourceListUsesCompactIdentityColumns(t *testing.T) {
	setOutputFormat(t, "text")

	output := captureStdout(t, func() {
		err := printDatasourceList(&operations.Response{
			Data: map[string]any{
				"datasources": []any{
					map[string]any{
						"type":        "clickhouse",
						"name":        "warehouse",
						"description": "long operational notes",
						"url":         "http://example.invalid",
					},
				},
			},
		})
		require.NoError(t, err)
	})

	assert.Contains(t, output, "DATASOURCE")
	assert.Contains(t, output, "TYPE")
	assert.Contains(t, output, "clickhouse")
	assert.Contains(t, output, "warehouse")
	assert.NotContains(t, output, "long operational notes")
	assert.NotContains(t, output, "http://example.invalid")
}

func TestPrintFilteredAPIStringValuesFiltersAndLimits(t *testing.T) {
	output := captureStdout(t, func() {
		err := printFilteredAPIStringValues(
			[]byte(`{"status":"success","data":["alpha_total","beta_total","alpha_count"]}`),
			"alpha",
			1,
		)
		require.NoError(t, err)
	})

	assert.Contains(t, output, "alpha_total")
	assert.NotContains(t, output, "beta_total")
	assert.NotContains(t, output, "alpha_count")
}

func TestIntFromAny(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  int64
	}{
		{name: "float64", value: float64(42), want: 42},
		{name: "int", value: 7, want: 7},
		{name: "int64", value: int64(99), want: 99},
		{name: "string is zero", value: "123", want: 0},
		{name: "nil is zero", value: nil, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, intFromAny(tt.value))
		})
	}
}

func TestNestedMap(t *testing.T) {
	t.Run("empty key asserts directly", func(t *testing.T) {
		value := map[string]any{"a": 1}
		assert.Equal(t, value, nestedMap(value, ""))
	})

	t.Run("traverses one level", func(t *testing.T) {
		value := map[string]any{"data": map[string]any{"head_slot": 100}}
		assert.Equal(t, map[string]any{"head_slot": 100}, nestedMap(value, "data"))
	})

	t.Run("missing key returns nil", func(t *testing.T) {
		value := map[string]any{"data": map[string]any{}}
		assert.Nil(t, nestedMap(value, "missing"))
	})

	t.Run("non-map value returns nil", func(t *testing.T) {
		assert.Nil(t, nestedMap("not a map", "data"))
	})
}
