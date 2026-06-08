package cli

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
			SimilarityScore: 0.8,
		}})
	})

	assert.Contains(t, output, "  Target: prometheus")
	assert.NotContains(t, output, "Cluster:")
}

func TestServerErrorHintUnknownIdentifier(t *testing.T) {
	hint := serverErrorHint(
		http.StatusNotFound,
		"Code: 47. DB::Exception: Unknown expression identifier `slot`. (UNKNOWN_IDENTIFIER)",
	)

	require.Contains(t, hint, "ClickHouse does not recognize")
	require.Contains(t, hint, "panda schema")
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
