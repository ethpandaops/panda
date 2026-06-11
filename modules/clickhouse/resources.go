package clickhouse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/module"
	"github.com/ethpandaops/panda/pkg/surface"
	"github.com/ethpandaops/panda/pkg/types"
)

// TablesListResponse is the response for the table listing resources
// (clickhouse://tables/{cluster} and .../{cluster}/{database}).
type TablesListResponse struct {
	Description string                           `json:"description"`
	Clusters    map[string]*ClusterTablesSummary `json:"clusters"`
	Usage       string                           `json:"usage"`
}

// ClusterTablesSummary is a compact summary of tables in a cluster.
type ClusterTablesSummary struct {
	Databases     []*DatabaseSummary `json:"databases,omitempty"`
	Tables        []*TableSummary    `json:"tables,omitempty"`
	TableCount    int                `json:"table_count"`
	DatabaseCount int                `json:"database_count,omitempty"`
	LastUpdated   string             `json:"last_updated"`
}

// DatabaseSummary is a compact overview of a database/table namespace.
type DatabaseSummary struct {
	Name       string `json:"name"`
	TableCount int    `json:"table_count"`
}

// TableSummary is a compact overview of a table for the list view.
type TableSummary struct {
	Database    string `json:"database"`
	Name        string `json:"name"`
	ColumnCount int    `json:"column_count"`
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

	reg.RegisterTemplate(types.TemplateResource{
		Template: mcp.NewResourceTemplate(
			"clickhouse://tables/{cluster}",
			"ClickHouse Cluster Databases",
			mcp.WithTemplateDescription("Compact database/table-namespace summary for a ClickHouse datasource/cluster. {cluster} is a datasource name, not a dataset id. Read clickhouse://tables/{cluster}/{database} for table names."),
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
			mcp.WithTemplateDescription("List the tables in a single ClickHouse database/namespace within a datasource/cluster. Dataset ids are guides; use concrete database names here."),
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
			mcp.WithTemplateDescription("Full schema for a specific ClickHouse table identified by concrete (cluster, database, table) names — columns, types, comments, engine, and key clauses."),
			mcp.WithTemplateMIMEType("application/json"),
			mcp.WithTemplateAnnotations([]mcp.Role{mcp.RoleAssistant}, 0.6, ""),
		),
		Pattern: regexp.MustCompile(`^clickhouse://tables/([^/]+)/([^/]+)/([^/]+)$`),
		Handler: createTableDetailHandler(log, client),
	})

	log.Debug("Registered ClickHouse schema resources")
}

func createClusterTablesHandler(client SchemaClient) types.ReadHandler {
	return func(ctx context.Context, uri string, _ surface.Dialect) (string, error) {
		parts := tableURISegments(uri)
		if len(parts) != 1 {
			return "", fmt.Errorf("invalid cluster URI: %s", uri)
		}

		clusterName := parts[0]

		cluster, ok := client.GetClusterTables(clusterName)
		if !ok {
			var fetchErr error
			cluster, fetchErr = client.FetchTablesInCluster(ctx, clusterName, "")
			if fetchErr != nil {
				if errors.Is(fetchErr, ErrSchemaClusterNotConfigured) {
					return "", clusterNotFoundError(client, clusterName)
				}

				return "", fmt.Errorf("listing live tables in cluster %q: %w", clusterName, fetchErr)
			}
		}

		response := &TablesListResponse{
			Description: fmt.Sprintf("Databases in ClickHouse cluster %q.", clusterName),
			Clusters:    map[string]*ClusterTablesSummary{clusterName: buildClusterDatabaseSummary(cluster)},
			Usage:       "Use concrete ClickHouse identifiers: {cluster} is a datasource name, and {database} is a ClickHouse database/namespace. Read clickhouse://tables/{cluster}/{database} for table names, then clickhouse://tables/{cluster}/{database}/{table_name} for detailed schema. The cluster view intentionally omits table names to keep output bounded.",
		}

		if clusterTablesIncludeTables(uri) {
			response.Description = fmt.Sprintf("Tables in ClickHouse cluster %q, keyed by (database, table).", clusterName)
			response.Clusters[clusterName] = buildClusterSummary(cluster, "")
			response.Usage = "Read clickhouse://tables/{cluster}/{database}/{table_name} for detailed schema. Prefer the compact cluster URI or database-scoped URI unless you need every table name. Dataset ids are guide names, not schema path segments."
		}

		return marshalResource(response, "cluster tables")
	}
}

func createDatabaseTablesHandler(client SchemaClient) types.ReadHandler {
	return func(ctx context.Context, uri string, _ surface.Dialect) (string, error) {
		parts := tableURISegments(uri)
		if len(parts) != 2 {
			return "", fmt.Errorf("invalid database URI: %s", uri)
		}

		clusterName, database := parts[0], parts[1]

		cluster, ok := client.GetClusterTables(clusterName)
		if !ok {
			var fetchErr error
			cluster, fetchErr = client.FetchTablesInCluster(ctx, clusterName, database)
			if fetchErr != nil {
				if errors.Is(fetchErr, ErrSchemaClusterNotConfigured) {
					return "", clusterNotFoundError(client, clusterName)
				}

				return "", fmt.Errorf("listing live tables in database %q of cluster %q: %w", database, clusterName, fetchErr)
			}
		}

		response := &TablesListResponse{
			Description: fmt.Sprintf("Tables in database %q of ClickHouse cluster %q.", database, clusterName),
			Clusters:    map[string]*ClusterTablesSummary{clusterName: buildClusterSummary(cluster, database)},
			Usage:       "Read clickhouse://tables/{cluster}/{database}/{table_name} for the detailed schema. Use concrete table names from this list.",
		}

		return marshalResource(response, "database tables")
	}
}

func createTableDetailHandler(log logrus.FieldLogger, client SchemaClient) types.ReadHandler {
	return func(ctx context.Context, uri string, _ surface.Dialect) (string, error) {
		parts := tableURISegments(uri)
		if len(parts) != 3 {
			return "", fmt.Errorf("invalid table URI: %s", uri)
		}

		clusterName, database, tableName := parts[0], parts[1], parts[2]

		schema, ok := client.GetTableInCluster(clusterName, database, tableName)
		if !ok {
			var fetchErr error
			schema, fetchErr = client.FetchTableInCluster(ctx, clusterName, database, tableName)
			if fetchErr == nil {
				data, err := marshalResource(&TableDetailResponse{Cluster: clusterName, Table: schema}, "table detail")
				if err != nil {
					return "", err
				}

				log.WithFields(logrus.Fields{
					"cluster":  clusterName,
					"database": database,
					"table":    tableName,
				}).Debug("Returned live table schema")

				return data, nil
			}

			if _, clusterOK := client.GetClusterTables(clusterName); !clusterOK {
				if errors.Is(fetchErr, ErrSchemaClusterNotConfigured) {
					return "", clusterNotFoundError(client, clusterName)
				}
			}

			return "", fmt.Errorf("table %q in database %q not found in cluster %q: %w", tableName, database, clusterName, fetchErr)
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
			Database:    schema.Database,
			Name:        schema.Name,
			ColumnCount: len(schema.Columns),
		})
	}

	summary.TableCount = len(summary.Tables)
	if databaseFilter != "" && summary.TableCount > 0 {
		summary.DatabaseCount = 1
	}

	sort.Slice(summary.Tables, func(i, j int) bool {
		if summary.Tables[i].Database != summary.Tables[j].Database {
			return summary.Tables[i].Database < summary.Tables[j].Database
		}

		return summary.Tables[i].Name < summary.Tables[j].Name
	})

	return summary
}

// buildClusterDatabaseSummary returns a compact view that avoids dumping every
// table in large multi-network clusters.
func buildClusterDatabaseSummary(cluster *ClusterTables) *ClusterTablesSummary {
	counts := make(map[string]int)
	for _, schema := range cluster.Tables {
		if schema == nil {
			continue
		}

		counts[schema.Database]++
	}

	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Strings(names)

	summary := &ClusterTablesSummary{
		Databases:     make([]*DatabaseSummary, 0, len(names)),
		TableCount:    len(cluster.Tables),
		DatabaseCount: len(names),
		LastUpdated:   cluster.LastUpdated.Format("2006-01-02T15:04:05Z"),
	}

	for _, name := range names {
		summary.Databases = append(summary.Databases, &DatabaseSummary{
			Name:       name,
			TableCount: counts[name],
		})
	}

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
		return fmt.Errorf("cluster %q not found: no ClickHouse schema cache is available yet; live datasource names are listed in datasources://clickhouse — retry after discovery warms up", clusterName)
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
	rest, _, _ = strings.Cut(rest, "?")
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

func clusterTablesIncludeTables(uri string) bool {
	_, rawQuery, ok := strings.Cut(uri, "?")
	if !ok {
		return false
	}

	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return false
	}

	return values.Get("include") == "tables" || values.Get("view") == "tables"
}
