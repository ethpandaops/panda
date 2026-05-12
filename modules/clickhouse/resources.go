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

	"github.com/ethpandaops/panda/pkg/module"
	"github.com/ethpandaops/panda/pkg/types"
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
type TableSummary struct {
	Database      string `json:"database"`
	Name          string `json:"name"`
	ColumnCount   int    `json:"column_count"`
	HasNetworkCol bool   `json:"has_network_column"`
}

// ClusterTableLocation pairs a cluster name with the database the table is in.
type ClusterTableLocation struct {
	Name     string `json:"name"`
	Database string `json:"database"`
}

// TableDetailResponse is the response for clickhouse://tables/{database}/{table_name}.
type TableDetailResponse struct {
	Table    *TableSchema           `json:"table"`
	Clusters []ClusterTableLocation `json:"clusters"`
}

// RegisterSchemaResources registers ClickHouse schema resources with the registry.
func RegisterSchemaResources(
	log logrus.FieldLogger,
	reg module.ResourceRegistry,
	client ClickHouseSchemaClient,
) {
	log = log.WithField("resource", "clickhouse_schema")

	reg.RegisterStatic(types.StaticResource{
		Resource: mcp.NewResource(
			"clickhouse://tables",
			"ClickHouse Tables",
			mcp.WithResourceDescription("List all available ClickHouse tables across clusters. Each entry is keyed by (database, table)."),
			mcp.WithMIMEType("application/json"),
			mcp.WithAnnotations([]mcp.Role{mcp.RoleAssistant}, 0.7),
		),
		Handler: createTablesListHandler(client),
	})

	template := mcp.NewResourceTemplate(
		"clickhouse://tables/{database}/{table_name}",
		"ClickHouse Table Schema",
		mcp.WithTemplateDescription("Full schema for a specific ClickHouse table identified by (database, table) — columns, types, comments, engine."),
		mcp.WithTemplateMIMEType("application/json"),
		mcp.WithTemplateAnnotations([]mcp.Role{mcp.RoleAssistant}, 0.6),
	)

	reg.RegisterTemplate(types.TemplateResource{
		Template: template,
		Pattern:  regexp.MustCompile(`^clickhouse://tables/([^/]+)/([^/]+)$`),
		Handler:  createTableDetailHandler(log, client),
	})

	log.Debug("Registered ClickHouse schema resources")
}

func createTablesListHandler(client ClickHouseSchemaClient) types.ReadHandler {
	return func(_ context.Context, _ string) (string, error) {
		allTables := client.GetAllTables()

		response := &TablesListResponse{
			Description: "Available ClickHouse tables across clusters. Each entry is keyed by (database, table).",
			Clusters:    make(map[string]*ClusterTablesSummary, len(allTables)),
			Usage:       "Read clickhouse://tables/{database}/{table_name} for the detailed schema.",
		}

		for clusterName, cluster := range allTables {
			summary := &ClusterTablesSummary{
				Tables:      make([]*TableSummary, 0, len(cluster.Tables)),
				TableCount:  len(cluster.Tables),
				LastUpdated: cluster.LastUpdated.Format("2006-01-02T15:04:05Z"),
			}

			for _, schema := range cluster.Tables {
				summary.Tables = append(summary.Tables, &TableSummary{
					Database:      schema.Database,
					Name:          schema.Name,
					ColumnCount:   len(schema.Columns),
					HasNetworkCol: schema.HasNetworkCol,
				})
			}

			sort.Slice(summary.Tables, func(i, j int) bool {
				if summary.Tables[i].Database != summary.Tables[j].Database {
					return summary.Tables[i].Database < summary.Tables[j].Database
				}

				return summary.Tables[i].Name < summary.Tables[j].Name
			})

			response.Clusters[clusterName] = summary
		}

		data, err := json.MarshalIndent(response, "", "  ")
		if err != nil {
			return "", fmt.Errorf("marshaling tables list: %w", err)
		}

		return string(data), nil
	}
}

func createTableDetailHandler(log logrus.FieldLogger, client ClickHouseSchemaClient) types.ReadHandler {
	return func(_ context.Context, uri string) (string, error) {
		database, tableName := extractQualifiedTableName(uri)
		if database == "" || tableName == "" {
			return "", fmt.Errorf("invalid table URI: %s", uri)
		}

		matches := client.GetTableExact(database, tableName)

		if len(matches) == 0 {
			return "", fmt.Errorf("table %q in database %q not found", tableName, database)
		}

		sort.Slice(matches, func(i, j int) bool {
			return matches[i].ClusterName < matches[j].ClusterName
		})

		base := *matches[0].Schema

		clusters := make([]ClusterTableLocation, 0, len(matches))

		for _, m := range matches {
			clusters = append(clusters, ClusterTableLocation{
				Name:     m.ClusterName,
				Database: m.Schema.Database,
			})
		}

		data, err := json.MarshalIndent(&TableDetailResponse{Table: &base, Clusters: clusters}, "", "  ")
		if err != nil {
			return "", fmt.Errorf("marshaling table detail: %w", err)
		}

		clusterNames := make([]string, 0, len(clusters))
		for _, c := range clusters {
			clusterNames = append(clusterNames, c.Name)
		}

		log.WithFields(logrus.Fields{
			"database": database,
			"table":    tableName,
			"clusters": clusterNames,
		}).Debug("Returned table schema")

		return string(data), nil
	}
}

func extractQualifiedTableName(uri string) (string, string) {
	prefix := "clickhouse://tables/"
	if !strings.HasPrefix(uri, prefix) {
		return "", ""
	}

	rest := strings.TrimPrefix(uri, prefix)

	idx := strings.Index(rest, "/")
	if idx <= 0 || idx == len(rest)-1 {
		return "", ""
	}

	database := rest[:idx]
	table := rest[idx+1:]

	if strings.Contains(table, "/") {
		return "", ""
	}

	return database, table
}
