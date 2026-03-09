package clickhouse

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/mcp/pkg/extension"
	"github.com/ethpandaops/mcp/pkg/types"
)

// TablesListResponse is the response for clickhouse://tables.
type TablesListResponse struct {
	Description string                           `json:"description"`
	Clusters    map[string]*ClusterTablesSummary `json:"clusters"`
	Usage       string                           `json:"usage"`
}

// ClusterTablesSummary is a compact summary of tables in a cluster.
type ClusterTablesSummary struct {
	Tables      []*TableSummary `json:"tables"`
	TableCount  int             `json:"table_count"`
	LastUpdated string          `json:"last_updated"`
}

// TableSummary is a compact overview of a table for the list view.
// Use clickhouse://tables/{table_name} for detailed schema including networks.
type TableSummary struct {
	Name          string `json:"name"`
	ColumnCount   int    `json:"column_count"`
	HasNetworkCol bool   `json:"has_network_column"`
}

// TableDetailResponse is the response for clickhouse://tables/{table_name}.
type TableDetailResponse struct {
	Table   *TableSchema `json:"table"`
	Cluster string       `json:"cluster"`
}

// RegisterSchemaResources registers ClickHouse schema resources with the registry.
func RegisterSchemaResources(
	log logrus.FieldLogger,
	reg extension.ResourceRegistry,
	client ClickHouseSchemaClient,
) {
	log = log.WithField("resource", "clickhouse_schema")

	// clickhouse://tables - List all tables across clusters
	reg.RegisterStatic(types.StaticResource{
		Resource: mcp.NewResource(
			"clickhouse://tables",
			"ClickHouse Tables",
			mcp.WithResourceDescription("List all available ClickHouse tables across xatu clusters with brief schema info"),
			mcp.WithMIMEType("application/json"),
			mcp.WithAnnotations([]mcp.Role{mcp.RoleAssistant}, 0.7),
		),
		Handler: createTablesListHandler(client),
	})

	// clickhouse://tables/{table_name} - Individual table details
	template := mcp.NewResourceTemplate(
		"clickhouse://tables/{table_name}",
		"ClickHouse Table Schema",
		mcp.WithTemplateDescription("Full schema for a specific ClickHouse table including columns, types, comments, and available networks"),
		mcp.WithTemplateMIMEType("application/json"),
		mcp.WithTemplateAnnotations([]mcp.Role{mcp.RoleAssistant}, 0.6),
	)

	reg.RegisterTemplate(types.TemplateResource{
		Template: template,
		Pattern:  regexp.MustCompile(`^clickhouse://tables/(.+)$`),
		Handler:  createTableDetailHandler(log, client),
	})

	log.Debug("Registered ClickHouse schema resources")
}

// createTablesListHandler creates a handler for the clickhouse://tables resource.
func createTablesListHandler(client ClickHouseSchemaClient) types.ReadHandler {
	return func(_ context.Context, _ string) (string, error) {
		allTables := client.GetAllTables()

		response := &TablesListResponse{
			Description: "Available ClickHouse tables across xatu clusters. Use clickhouse://tables/{table_name} for detailed schema.",
			Clusters:    make(map[string]*ClusterTablesSummary, len(allTables)),
			Usage:       "To get detailed schema for a table, access clickhouse://tables/{table_name}",
		}

		// Build cluster summaries
		for clusterName, cluster := range allTables {
			summary := &ClusterTablesSummary{
				Tables:      make([]*TableSummary, 0, len(cluster.Tables)),
				TableCount:  len(cluster.Tables),
				LastUpdated: cluster.LastUpdated.Format("2006-01-02T15:04:05Z"),
			}

			// Sort table names for consistent output
			tableNames := make([]string, 0, len(cluster.Tables))
			for tableName := range cluster.Tables {
				tableNames = append(tableNames, tableName)
			}

			sort.Strings(tableNames)

			for _, tableName := range tableNames {
				schema := cluster.Tables[tableName]

				tableSummary := &TableSummary{
					Name:          schema.Name,
					ColumnCount:   len(schema.Columns),
					HasNetworkCol: schema.HasNetworkCol,
				}

				summary.Tables = append(summary.Tables, tableSummary)
			}

			response.Clusters[clusterName] = summary
		}

		data, err := json.MarshalIndent(response, "", "  ")
		if err != nil {
			return "", fmt.Errorf("marshaling tables list: %w", err)
		}

		return string(data), nil
	}
}

// createTableDetailHandler creates a handler for the clickhouse://tables/{table_name} resource.
func createTableDetailHandler(log logrus.FieldLogger, client ClickHouseSchemaClient) types.ReadHandler {
	return func(_ context.Context, uri string) (string, error) {
		// Extract table name from URI
		tableName := extractTableName(uri)
		if tableName == "" {
			return "", fmt.Errorf("invalid table URI: %s", uri)
		}

		schema, clusterName, found := client.GetTable(tableName)
		if !found {
			// Try to find a partial match (case-insensitive)
			allTables := client.GetAllTables()

			for cluster, clusterTables := range allTables {
				for name, tableSchema := range clusterTables.Tables {
					if strings.EqualFold(name, tableName) {
						schema = tableSchema
						clusterName = cluster
						found = true

						break
					}
				}

				if found {
					break
				}
			}
		}

		if !found {
			// List available tables in error message
			availableTables := listAvailableTables(client)

			return "", fmt.Errorf("table %q not found. Available tables: %s", tableName, strings.Join(availableTables, ", "))
		}

		response := &TableDetailResponse{
			Table:   schema,
			Cluster: clusterName,
		}

		data, err := json.MarshalIndent(response, "", "  ")
		if err != nil {
			return "", fmt.Errorf("marshaling table detail: %w", err)
		}

		log.WithFields(logrus.Fields{
			"table":   tableName,
			"cluster": clusterName,
		}).Debug("Returned table schema")

		return string(data), nil
	}
}

// extractTableName extracts the table name from a clickhouse://tables/{table_name} URI.
func extractTableName(uri string) string {
	prefix := "clickhouse://tables/"
	if !strings.HasPrefix(uri, prefix) {
		return ""
	}

	return strings.TrimPrefix(uri, prefix)
}

// listAvailableTables returns a sorted list of all available table names.
func listAvailableTables(client ClickHouseSchemaClient) []string {
	allTables := client.GetAllTables()
	tableNames := make([]string, 0, 64)

	for _, cluster := range allTables {
		for tableName := range cluster.Tables {
			tableNames = append(tableNames, tableName)
		}
	}

	sort.Strings(tableNames)

	// Deduplicate (in case same table exists in multiple clusters)
	unique := make([]string, 0, len(tableNames))

	prev := ""

	for _, name := range tableNames {
		if name != prev {
			unique = append(unique, name)
			prev = name
		}
	}

	return unique
}
