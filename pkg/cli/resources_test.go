package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/serverapi"
)

func TestRunResourcesReadsDirectURI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/resources/read" {
			t.Errorf("path = %q, want /api/v1/resources/read", r.URL.Path)
			http.NotFound(w, r)
			return
		}

		if got := r.URL.Query().Get("uri"); got != "panda://getting-started" {
			t.Errorf("uri = %q, want panda://getting-started", got)
			http.Error(w, "bad uri", http.StatusBadRequest)
			return
		}

		if got := r.URL.Query().Get("client_context"); got != "cli" {
			t.Errorf("client_context = %q, want cli", got)
			http.Error(w, "bad client_context", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "text/markdown")
		_, _ = fmt.Fprint(w, "guide content")
	}))
	defer server.Close()

	setClientConfig(t, server.URL)
	setOutputFormat(t, "text")

	output := captureStdout(t, func() {
		err := runResources(testCommand(), []string{"panda://getting-started"})
		require.NoError(t, err)
	})

	assert.Equal(t, "guide content", output)
}

func TestRunResourcesListsWithoutURI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/resources" {
			t.Errorf("path = %q, want /api/v1/resources", r.URL.Path)
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(serverapi.ListResourcesResponse{
			Resources: []serverapi.ResourceInfo{{
				URI:         "panda://getting-started",
				Name:        "Getting Started",
				Description: "Start here",
			}},
		}); err != nil {
			t.Errorf("encoding response: %v", err)
		}
	}))
	defer server.Close()

	setClientConfig(t, server.URL)
	setOutputFormat(t, "text")

	output := captureStdout(t, func() {
		err := runResources(testCommand(), nil)
		require.NoError(t, err)
	})

	assert.Contains(t, output, "Resources:")
	assert.Contains(t, output, "panda://getting-started")
	assert.Contains(t, output, "Read a resource: panda resources <uri>")
}

func TestResourcesReadHasGetAliasAndListSubcommand(t *testing.T) {
	assert.Contains(t, resourcesReadCmd.Aliases, "get")

	var foundList bool
	for _, cmd := range resourcesCmd.Commands() {
		if cmd.Name() == "list" {
			foundList = true
			break
		}
	}

	assert.True(t, foundList)
}

func TestServerErrorHintUsesExistingResourcesCommand(t *testing.T) {
	hint := serverErrorHint(http.StatusNotFound, "missing")

	assert.Contains(t, hint, "panda resources")
	assert.NotContains(t, hint, "panda resources list")
}

func TestServerErrorHintClassifiesClickHouseErrors(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		message string
		want    string
	}{
		{
			name:    "primary key not used",
			status:  http.StatusInternalServerError,
			message: "Code: 277. DB::Exception: Primary key (a, b) is not used and setting 'force_primary_key' is set. (INDEX_NOT_USED)",
			want:    "primary-key/order-key columns",
		},
		{
			name:    "unknown identifier",
			status:  http.StatusNotFound,
			message: "Code: 47. DB::Exception: Unknown expression identifier `missing_col`. (UNKNOWN_IDENTIFIER)",
			want:    "references a column or expression",
		},
		{
			name:    "not aggregate",
			status:  http.StatusInternalServerError,
			message: "Code: 215. DB::Exception: Column is not under aggregate function. (NOT_AN_AGGREGATE)",
			want:    "aggregated or included in GROUP BY",
		},
		{
			name:    "wrong clickhouse datasource",
			status:  http.StatusNotFound,
			message: "clickhouse datasource \"network\" not found",
			want:    "datasource/cluster name",
		},
		{
			name:    "wrong schema cluster",
			status:  http.StatusBadRequest,
			message: "reading template resource clickhouse://tables/xatu-cbt: cluster \"xatu-cbt\" not found: available clusters are clickhouse-refined",
			want:    "schema expects a ClickHouse datasource/cluster name",
		},
		{
			name:    "unknown dataset",
			status:  http.StatusBadRequest,
			message: "reading template resource datasets://warehouse: unknown dataset \"warehouse\". Known datasets: metrics",
			want:    "knowledge-pack IDs",
		},
		{
			name:    "distributed join denied",
			status:  http.StatusInternalServerError,
			message: "Code: 288. DB::Exception: Double-distributed IN/JOIN subqueries is denied. (DISTRIBUTED_IN_JOIN_SUBQUERY_DENIED)",
			want:    "distributed subquery or join",
		},
		{
			name:    "unknown table",
			status:  http.StatusNotFound,
			message: "Code: 60. DB::Exception: Unknown table expression identifier 'example_db.example_table'. (UNKNOWN_TABLE)",
			want:    "table or database",
		},
		{
			name:    "unknown function",
			status:  http.StatusNotFound,
			message: "Code: 46. DB::Exception: Function with name `exampleFn` does not exist. (UNKNOWN_FUNCTION)",
			want:    "function unavailable",
		},
		{
			name:    "bad function arguments",
			status:  http.StatusBadRequest,
			message: "Code: 36. DB::Exception: Functions lowerUTF8 cannot work with FixedString argument. (BAD_ARGUMENTS)",
			want:    "incompatible argument type",
		},
		{
			name:    "bad query parameter literal",
			status:  http.StatusBadRequest,
			message: "Code: 26. DB::Exception: Cannot parse quoted string. (CANNOT_PARSE_QUOTED_STRING)",
			want:    "incompatible argument type",
		},
		{
			name:    "illegal aggregation",
			status:  http.StatusInternalServerError,
			message: "Code: 184. DB::Exception: Aggregate function count() is found inside another aggregate function. (ILLEGAL_AGGREGATION)",
			want:    "nested inside other aggregate functions",
		},
		{
			name:    "alias required",
			status:  http.StatusInternalServerError,
			message: "Code: 206. DB::Exception: JOIN CROSS JOIN ... no alias for subquery. (ALIAS_REQUIRED)",
			want:    "explicit aliases",
		},
		{
			name:    "wrong prometheus datasource",
			status:  http.StatusNotFound,
			message: "prometheus datasource \"metrics\" not found",
			want:    "Prometheus argument",
		},
		{
			name:    "invalid schema path identifier",
			status:  http.StatusBadRequest,
			message: "reading template resource clickhouse://tables/cluster/dataset-name/table: validating database name: invalid identifier \"dataset-name\"",
			want:    "concrete ClickHouse identifiers",
		},
		{
			name:    "upstream bad gateway",
			status:  http.StatusBadGateway,
			message: "error code: 502",
			want:    "upstream datasource or node",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hint := serverErrorHint(tt.status, tt.message)
			assert.Contains(t, hint, tt.want)
			assert.NotContains(t, hint, "panda resources")
		})
	}
}

func setOutputFormat(t *testing.T, value string) {
	t.Helper()

	original := outputFormat
	outputFormat = value
	t.Cleanup(func() { outputFormat = original })
}

func testCommand() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	return cmd
}
