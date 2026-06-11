package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	clickhousemodule "github.com/ethpandaops/panda/modules/clickhouse"
)

var schemaContains string

var schemaCmd = &cobra.Command{
	GroupID: groupDiscovery,
	Use:     "schema [cluster] [database] [table]",
	Short:   "Show ClickHouse table schemas",
	Long: `Show available ClickHouse tables and their schemas, scoped by cluster.

Arguments progressively narrow the view:
  panda schema                              list the available clusters
  panda schema <cluster>                    list databases/table namespaces in one cluster
  panda schema <cluster> <database>         list the tables in one database
  panda schema <cluster> <database> <table> show the full schema for one table

Run 'panda datasources --type clickhouse' (or 'panda schema' with no arguments)
to see the available cluster names. On large databases, use --contains to narrow
table-name listings before opening a full table schema. With --contains,
cluster-level schema searches table names across the cluster.

Examples:
  panda schema
  panda schema <cluster>
  panda schema <cluster> <database> --contains '<term>'
  panda schema <cluster> <database> <table>
  panda schema --json`,
	Args: schemaArgs,
	RunE: runSchema,
}

func init() {
	rootCmd.AddCommand(schemaCmd)
	schemaCmd.Flags().StringVar(&schemaContains, "contains", "", "Case-insensitive table-name filter for table listings")
	schemaCmd.ValidArgsFunction = completeSchemaArgs
}

func runSchema(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	var (
		response     *clickhousemodule.TablesListResponse
		clusterScope bool
		err          error
	)

	switch len(args) {
	case 0:
		if schemaContainsSet() {
			return fmt.Errorf("--contains requires a cluster argument")
		}

		return renderSchemaClusterIndex(ctx)
	case 1:
		clusterScope = true
		response, err = readClickHouseClusterTables(ctx, args[0], schemaContainsSet())
	case 2:
		if database, table, ok := splitQualifiedTable(args[1]); ok {
			if schemaContainsSet() {
				return fmt.Errorf("--contains only applies to table listings; omit the table argument to filter names")
			}

			return showTable(ctx, args[0], database, table)
		}

		response, err = readClickHouseDatabaseTables(ctx, args[0], args[1])
	default:
		if schemaContainsSet() {
			return fmt.Errorf("--contains only applies to table listings; omit the table argument to filter names")
		}

		return showTable(ctx, args[0], args[1], args[2])
	}

	if err != nil {
		return schemaArgumentError(ctx, args, err)
	}

	return renderTablesList(response, clusterScope)
}

func schemaArgs(_ *cobra.Command, args []string) error {
	if len(args) > 3 {
		return fmt.Errorf("schema accepts at most <cluster> <database> <table>; omit SQL clauses such as FINAL and use them only in SELECT queries")
	}

	if hasSchemaTemplatePlaceholder(args) {
		return fmt.Errorf("schema arguments must be concrete ClickHouse identifiers; replace dataset placeholders such as {name} using the dataset guide before running schema")
	}

	return nil
}

func schemaArgumentError(ctx context.Context, args []string, err error) error {
	if len(args) == 0 || err == nil {
		return err
	}

	if len(args) == 1 && strings.Contains(args[0], ".") {
		return fmt.Errorf("schema arguments are space-separated: use 'panda schema <cluster> <database> <table>' for a known table, or 'panda schema <cluster> --contains <term>' to search table names")
	}

	if isActiveDatasetName(ctx, args[0]) {
		return fmt.Errorf("schema argument %q is a dataset guide name, not a ClickHouse cluster. Read 'panda datasets %s' for placement/syntax, then use an example Target or 'panda datasources --type clickhouse' value as <cluster>", args[0], args[0])
	}

	if len(args) >= 2 && isActiveDatasetName(ctx, args[1]) {
		return fmt.Errorf("schema database argument %q is a dataset guide name, not a ClickHouse database. Read 'panda datasets %s' for placement/syntax, then substitute concrete database and table identifiers", args[1], args[1])
	}

	return err
}

func hasSchemaTemplatePlaceholder(args []string) bool {
	for _, arg := range args {
		if strings.Contains(arg, "{") || strings.Contains(arg, "}") {
			return true
		}
	}

	return false
}

func splitQualifiedTable(value string) (string, string, bool) {
	parts := strings.Split(value, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}

	return parts[0], parts[1], true
}

func renderTablesList(response *clickhousemodule.TablesListResponse, clusterScope bool) error {
	if isJSON() {
		if schemaContainsSet() {
			response = filteredTablesListResponse(response, schemaContains)
		}

		return printJSON(response)
	}

	if clusterScope && !schemaContainsSet() {
		return renderClusterDatabaseList(response)
	}

	clusterNames := make([]string, 0, len(response.Clusters))
	for clusterName := range response.Clusters {
		clusterNames = append(clusterNames, clusterName)
	}
	sort.Strings(clusterNames)

	for _, clusterName := range clusterNames {
		cluster := response.Clusters[clusterName]
		tables := filterTablesByName(cluster.Tables, schemaContains)
		if schemaContainsSet() {
			fmt.Printf("Cluster: %s (%d matching of %d tables, updated %s)\n",
				clusterName, len(tables), cluster.TableCount, cluster.LastUpdated)
			fmt.Printf("Filter: table name contains %q\n", schemaContains)
		} else {
			fmt.Printf("Cluster: %s (%d tables, updated %s)\n", clusterName, cluster.TableCount, cluster.LastUpdated)
		}

		for _, table := range tables {
			fmt.Printf("  %-60s  %d cols\n", table.Database+"."+table.Name, table.ColumnCount)
		}

		if len(tables) == 0 {
			fmt.Println("  No matching tables.")
		}

		fmt.Println()
	}

	return nil
}

func filteredTablesListResponse(
	response *clickhousemodule.TablesListResponse,
	contains string,
) *clickhousemodule.TablesListResponse {
	if response == nil || strings.TrimSpace(contains) == "" {
		return response
	}

	filtered := *response
	filtered.Clusters = make(map[string]*clickhousemodule.ClusterTablesSummary, len(response.Clusters))

	for clusterName, cluster := range response.Clusters {
		if cluster == nil {
			filtered.Clusters[clusterName] = nil
			continue
		}

		clusterCopy := *cluster
		clusterCopy.Tables = filterTablesByName(cluster.Tables, contains)
		clusterCopy.TableCount = len(clusterCopy.Tables)
		filtered.Clusters[clusterName] = &clusterCopy
	}

	return &filtered
}

func renderClusterDatabaseList(response *clickhousemodule.TablesListResponse) error {
	clusterNames := make([]string, 0, len(response.Clusters))
	for clusterName := range response.Clusters {
		clusterNames = append(clusterNames, clusterName)
	}
	sort.Strings(clusterNames)

	for _, clusterName := range clusterNames {
		cluster := response.Clusters[clusterName]
		if cluster == nil {
			fmt.Printf("Cluster: %s (no schema summary)\n\n", clusterName)
			continue
		}

		rows := clusterDatabaseRows(cluster)
		databaseCount := cluster.DatabaseCount
		if databaseCount == 0 {
			databaseCount = len(rows)
		}

		fmt.Printf("Cluster: %s (%d tables across %d databases, updated %s)\n",
			clusterName, cluster.TableCount, databaseCount, cluster.LastUpdated)

		if len(rows) == 0 {
			fmt.Println("  No databases found.")
		} else {
			printTable([]string{"DATABASE", "TABLES"}, rows)
		}

		fmt.Printf("\nUse: panda schema %s <database> [--contains <term>]\n", clusterName)
		fmt.Printf("Or search table names across the cluster: panda schema %s --contains <term>\n\n", clusterName)
	}

	return nil
}

func clusterDatabaseRows(cluster *clickhousemodule.ClusterTablesSummary) [][]string {
	if cluster == nil {
		return nil
	}

	if len(cluster.Databases) > 0 {
		rows := make([][]string, 0, len(cluster.Databases))
		for _, database := range cluster.Databases {
			if database == nil {
				continue
			}

			rows = append(rows, []string{database.Name, fmt.Sprintf("%d", database.TableCount)})
		}

		sort.Slice(rows, func(i, j int) bool { return rows[i][0] < rows[j][0] })
		return rows
	}

	counts := make(map[string]int)
	for _, table := range cluster.Tables {
		if table == nil {
			continue
		}

		counts[table.Database]++
	}

	databases := make([]string, 0, len(counts))
	for database := range counts {
		databases = append(databases, database)
	}
	sort.Strings(databases)

	rows := make([][]string, 0, len(databases))
	for _, database := range databases {
		rows = append(rows, []string{database, fmt.Sprintf("%d", counts[database])})
	}

	return rows
}

func schemaContainsSet() bool {
	return strings.TrimSpace(schemaContains) != ""
}

func filterTablesByName(tables []*clickhousemodule.TableSummary, contains string) []*clickhousemodule.TableSummary {
	needle := strings.ToLower(strings.TrimSpace(contains))
	if needle == "" {
		return tables
	}

	filtered := make([]*clickhousemodule.TableSummary, 0, len(tables))
	for _, table := range tables {
		if table == nil {
			continue
		}

		qualified := table.Database + "." + table.Name
		if strings.Contains(strings.ToLower(qualified), needle) {
			filtered = append(filtered, table)
		}
	}

	return filtered
}

func showTable(ctx context.Context, cluster, database, table string) error {
	response, err := readClickHouseTable(ctx, cluster, database, table)
	if err != nil {
		return err
	}

	if isJSON() {
		return printJSON(response)
	}

	schema := response.Table

	fmt.Printf("Table: %s.%s  (cluster: %s)\n", schema.Database, schema.Name, response.Cluster)

	if schema.Comment != "" {
		fmt.Printf("Comment: %s\n", schema.Comment)
	}

	if schema.Engine != "" {
		fmt.Printf("Engine: %s\n", schema.Engine)
	}

	if schema.PartitionBy != "" || schema.OrderBy != "" || schema.PrimaryKey != "" {
		fmt.Println()
		fmt.Println("Keys:")

		if schema.PartitionBy != "" {
			fmt.Printf("  Partition by: %s\n", schema.PartitionBy)
		}

		if schema.OrderBy != "" {
			fmt.Printf("  Order by: %s\n", schema.OrderBy)
		}

		if schema.PrimaryKey != "" {
			fmt.Printf("  Primary key: %s\n", schema.PrimaryKey)
		}
	}

	fmt.Println()

	rows := make([][]string, 0, len(schema.Columns))
	for _, col := range schema.Columns {
		rows = append(rows, []string{col.Name, col.Type, col.Comment})
	}

	printTable([]string{"NAME", "TYPE", "COMMENT"}, rows)

	return nil
}

// renderSchemaClusterIndex lists the ClickHouse clusters and how to drill into
// them. The full all-clusters table dump no longer exists: it was unbounded on
// deployments with per-network databases.
func renderSchemaClusterIndex(ctx context.Context) error {
	response, err := listDatasources(ctx, "clickhouse")
	if err != nil {
		return fmt.Errorf("listing ClickHouse datasources: %w", err)
	}

	if len(response.Datasources) == 0 {
		fmt.Println("No ClickHouse datasources found.")

		return nil
	}

	fmt.Println("Specify a cluster: panda schema <cluster> [database] [table]")
	fmt.Println()

	rows := make([][]string, 0, len(response.Datasources))
	for _, info := range response.Datasources {
		rows = append(rows, []string{info.Name, info.Description})
	}

	sort.Slice(rows, func(i, j int) bool { return rows[i][0] < rows[j][0] })

	printTable([]string{"CLUSTER", "DESCRIPTION"}, rows)
	fmt.Println("\nUse: panda schema <cluster> <database> [--contains <term>]")
	fmt.Println("Or read schema resources directly: panda resources clickhouse://tables/<cluster>/<database>/<table>")
	fmt.Println("Cluster names are ClickHouse datasource names, not dataset names.")

	return nil
}
