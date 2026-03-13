package cli

import (
	"net/http"
	"testing"

	clickhousemodule "github.com/ethpandaops/panda/modules/clickhouse"
	"github.com/ethpandaops/panda/pkg/operations"
	"github.com/ethpandaops/panda/pkg/serverapi"
	"github.com/ethpandaops/panda/pkg/types"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
)

func TestCompletionHelpersReturnExpectedNames(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/datasources", func(w http.ResponseWriter, r *http.Request) {
		writeJSONResponse(t, w, http.StatusOK, serverapi.DatasourcesResponse{
			Datasources: []types.DatasourceInfo{
				{Type: "clickhouse", Name: "xatu"},
				{Type: "clickhouse", Name: "xatu-cbt"},
			},
		})
	})
	mux.HandleFunc("/api/v1/sessions", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(t, w, http.StatusOK, serverapi.ListSessionsResponse{
			Sessions: []serverapi.SessionResponse{
				{SessionID: "sess-1"},
				{SessionID: "sess-2"},
			},
		})
	})
	mux.HandleFunc("/api/v1/resources/read", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("uri") {
		case "clickhouse://tables":
			writeJSONResponse(t, w, http.StatusOK, clickhousemodule.TablesListResponse{
				Clusters: map[string]*clickhousemodule.ClusterTablesSummary{
					"xatu": {
						Tables: []*clickhousemodule.TableSummary{
							{Name: "blocks"},
							{Name: "slots"},
						},
					},
				},
			})
		case "dora://networks":
			writeJSONResponse(t, w, http.StatusOK, operations.DoraNetworksPayload{
				Networks: []operations.DoraNetwork{
					{Name: "hoodi"},
					{Name: ""},
					{Name: "mainnet"},
				},
			})
		default:
			http.NotFound(w, r)
		}
	})
	mux.HandleFunc("/api/v1/operations/dora.list_networks", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(t, w, http.StatusOK, operations.NewObjectResponse(operations.DoraNetworksPayload{
			Networks: []operations.DoraNetwork{
				{Name: "hoodi"},
				{Name: ""},
				{Name: "mainnet"},
			},
		}, nil))
	})

	newCLIHarness(t, mux)

	names, directive := completeDatasourceNames("clickhouse")(datasourcesCmd, nil, "")
	assert.Equal(t, []string{"xatu", "xatu-cbt"}, names)
	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)

	names, directive = completeDatasourceNames("clickhouse")(datasourcesCmd, []string{"already"}, "")
	assert.Nil(t, names)
	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)

	sessionIDs, directive := completeSessionIDs(datasourcesCmd, nil, "")
	assert.Equal(t, []string{"sess-1", "sess-2"}, sessionIDs)
	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)

	tableNames, directive := completeTableNames(datasourcesCmd, nil, "")
	assert.Equal(t, []string{"blocks", "slots"}, tableNames)
	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)

	networkNames, directive := completeNetworkNames(datasourcesCmd, nil, "")
	assert.Equal(t, []string{"hoodi", "mainnet"}, networkNames)
	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)

	noValues, directive := noCompletions(datasourcesCmd, nil, "")
	assert.Nil(t, noValues)
	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)
}
