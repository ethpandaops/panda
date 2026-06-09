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

// TablesListResponse is the response for the table listing resources
// (clickhouse://tables, .../{cluster}, and .../{cluster}/{database}).
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

// TableDetailResponse is the response for
// clickhouse://tables/{cluster}/{database}/{table_name}.
type TableDetailResponse struct {
	Cluster string       `json:"cluster"`
	Table   *TableSchema `json:"table"`
}

// RegisterSchemaResources registers ClickHouse schema resources with the registry.
func RegisterSchemaResources(
	log logrus.FieldLogger,
	reg module.ResourceRegistry,
	client SchemaClient,
) {
	log = log.WithField("resource", "clickhouse_schema")

	reg.RegisterStatic(types.StaticResource{
		Resource: mcp.NewResource(
			"clickhouse://tables",
			"ClickHouse Tables",
			mcp.WithResourceDescription("List all available ClickHouse tables, grouped by cluster. Each entry is keyed by (database, table)."),
			mcp.WithMIMEType("application/json"),
			mcp.WithAnnotations([]mcp.Role{mcp.RoleAssistant}, 0.7, ""),
		),
		Handler: createTablesListHandler(client),
	})

	reg.RegisterTemplate(types.TemplateResource{
		Template: mcp.NewResourceTemplate(
			"clickhouse://tables/{cluster}",
			"ClickHouse Cluster Tables",
			mcp.WithTemplateDescription("List the tables in a single ClickHouse cluster, keyed by (database, table)."),
			mcp.WithTemplateMIMEType("application/json"),
			mcp.WithTemplateAnnotations([]mcp.Role{mcp.RoleAssistant}, 0.6, ""),
		),
		Pattern: regexp.MustCompile(`^clickhouse://tables/([^/]+)$`),
		Handler: createClusterTablesHandler(client),
	})

	reg.RegisterTemplate(types.TemplateResource{
		Template: mcp.NewResourceTemplate(
			"clickhouse://tables/{cluster}/{database}",
			"ClickHouse Database Tables",
			mcp.WithTemplateDescription("List the tables in a single database within a ClickHouse cluster."),
			mcp.WithTemplateMIMEType("application/json"),
			mcp.WithTemplateAnnotations([]mcp.Role{mcp.RoleAssistant}, 0.6, ""),
		),
		Pattern: regexp.MustCompile(`^clickhouse://tables/([^/]+)/([^/]+)$`),
		Handler: createDatabaseTablesHandler(client),
	})

	reg.RegisterTemplate(types.TemplateResource{
		Template: mcp.NewResourceTemplate(
			"clickhouse://tables/{cluster}/{database}/{table_name}",
			"ClickHouse Table Schema",
			mcp.WithTemplateDescription("Full schema for a specific ClickHouse table identified by (cluster, database, table) — columns, types, comments, engine."),
			mcp.WithTemplateMIMEType("application/json"),
			mcp.WithTemplateAnnotations([]mcp.Role{mcp.RoleAssistant}, 0.6, ""),
		),
		Pattern: regexp.MustCompile(`^clickhouse://tables/([^/]+)/([^/]+)/([^/]+)$`),
		Handler: createTableDetailHandler(log, client),
	})

	log.Debug("Registered ClickHouse schema resources")
}

func createTablesListHandler(client SchemaClient) types.ReadHandler {
	return func(_ context.Context, _ string) (string, error) {
		allTables := client.GetAllTables()

		response := &TablesListResponse{
			Description: "Available ClickHouse tables, grouped by cluster. Each entry is keyed by (database, table).",
			Clusters:    make(map[string]*ClusterTablesSummary, len(allTables)),
			Usage:       "Read clickhouse://tables/{cluster}/{database}/{table_name} for the detailed schema.",
		}

		for clusterName, cluster := range allTables {
			response.Clusters[clusterName] = buildClusterSummary(cluster, "")
		}

		return marshalResource(response, "tables list")
	}
}

func createClusterTablesHandler(client SchemaClient) types.ReadHandler {
	return func(_ context.Context, uri string) (string, error) {
		parts := tableURISegments(uri)
		if len(parts) != 1 {
			return "", fmt.Errorf("invalid cluster URI: %s", uri)
		}

		clusterName := parts[0]

		cluster, ok := client.GetClusterTables(clusterName)
		if !ok {
			return "", clusterNotFoundError(client, clusterName)
		}

		response := &TablesListResponse{
			Description: fmt.Sprintf("Tables in ClickHouse cluster %q, keyed by (database, table).", clusterName),
			Clusters:    map[string]*ClusterTablesSummary{clusterName: buildClusterSummary(cluster, "")},
			Usage:       "Read clickhouse://tables/{cluster}/{database}/{table_name} for the detailed schema.",
		}

		return marshalResource(response, "cluster tables")
	}
}

func createDatabaseTablesHandler(client SchemaClient) types.ReadHandler {
	return func(_ context.Context, uri string) (string, error) {
		parts := tableURISegments(uri)
		if len(parts) != 2 {
			return "", fmt.Errorf("invalid database URI: %s", uri)
		}

		clusterName, database := parts[0], parts[1]

		cluster, ok := client.GetClusterTables(clusterName)
		if !ok {
			return "", clusterNotFoundError(client, clusterName)
		}

		response := &TablesListResponse{
			Description: fmt.Sprintf("Tables in database %q of ClickHouse cluster %q.", database, clusterName),
			Clusters:    map[string]*ClusterTablesSummary{clusterName: buildClusterSummary(cluster, database)},
			Usage:       "Read clickhouse://tables/{cluster}/{database}/{table_name} for the detailed schema.",
		}

		return marshalResource(response, "database tables")
	}
}

func createTableDetailHandler(log logrus.FieldLogger, client SchemaClient) types.ReadHandler {
	return func(_ context.Context, uri string) (string, error) {
		parts := tableURISegments(uri)
		if len(parts) != 3 {
			return "", fmt.Errorf("invalid table URI: %s", uri)
		}

		clusterName, database, tableName := parts[0], parts[1], parts[2]

		schema, ok := client.GetTableInCluster(clusterName, database, tableName)
		if !ok {
			if _, clusterOK := client.GetClusterTables(clusterName); !clusterOK {
				return "", clusterNotFoundError(client, clusterName)
			}

			return "", fmt.Errorf("table %q in database %q not found in cluster %q", tableName, database, clusterName)
		}

		data, err := marshalResource(&TableDetailResponse{Cluster: clusterName, Table: schema}, "table detail")
		if err != nil {
			return "", err
		}

		log.WithFields(logrus.Fields{
			"cluster":  clusterName,
			"database": database,
			"table":    tableName,
		}).Debug("Returned table schema")

		return data, nil
	}
}

// buildClusterSummary builds a compact summary of a cluster's tables, optionally
// restricted to a single database.
func buildClusterSummary(cluster *ClusterTables, databaseFilter string) *ClusterTablesSummary {
	summary := &ClusterTablesSummary{
		Tables:      make([]*TableSummary, 0, len(cluster.Tables)),
		LastUpdated: cluster.LastUpdated.Format("2006-01-02T15:04:05Z"),
	}

	for _, schema := range cluster.Tables {
		if databaseFilter != "" && schema.Database != databaseFilter {
			continue
		}

		summary.Tables = append(summary.Tables, &TableSummary{
			Database:      schema.Database,
			Name:          schema.Name,
			ColumnCount:   len(schema.Columns),
			HasNetworkCol: schema.HasNetworkCol,
		})
	}

	summary.TableCount = len(summary.Tables)

	sort.Slice(summary.Tables, func(i, j int) bool {
		if summary.Tables[i].Database != summary.Tables[j].Database {
			return summary.Tables[i].Database < summary.Tables[j].Database
		}

		return summary.Tables[i].Name < summary.Tables[j].Name
	})

	return summary
}

// clusterNotFoundError builds an error that names the clusters that do exist.
func clusterNotFoundError(client SchemaClient, clusterName string) error {
	available := make([]string, 0, len(client.GetAllTables()))
	for name := range client.GetAllTables() {
		available = append(available, name)
	}

	sort.Strings(available)

	if len(available) == 0 {
		return fmt.Errorf("cluster %q not found: no ClickHouse clusters are available", clusterName)
	}

	return fmt.Errorf("cluster %q not found: available clusters are %s", clusterName, strings.Join(available, ", "))
}

func marshalResource(payload any, what string) (string, error) {
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshaling %s: %w", what, err)
	}

	return string(data), nil
}

// tableURISegments returns the path segments after the clickhouse://tables/
// prefix, or nil if the URI is malformed (wrong prefix or empty segments).
func tableURISegments(uri string) []string {
	const prefix = "clickhouse://tables/"

	if !strings.HasPrefix(uri, prefix) {
		return nil
	}

	rest := strings.TrimPrefix(uri, prefix)
	if rest == "" {
		return nil
	}

	parts := strings.Split(rest, "/")
	for _, p := range parts {
		if p == "" {
			return nil
		}
	}

	return parts
}
