package cli

import (
	"context"

	"github.com/spf13/cobra"
)

// completeDatasourceNames completes the first positional arg with datasource names.
func completeDatasourceNames(dsType string) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		response, err := listDatasources(commandContext(cmd), dsType)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		names := make([]string, 0, len(response.Datasources))
		for _, ds := range response.Datasources {
			names = append(names, ds.Name)
		}

		return names, cobra.ShellCompDirectiveNoFileComp
	}
}

// completeNetworkNames completes the first positional arg with network names.
func completeNetworkNames(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	response, err := runServerOperation(cmd, "dora.list_networks", map[string]any{})
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	data, _ := response.Data.(map[string]any)
	items, _ := data["networks"].([]any)

	names := make([]string, 0, len(items))
	for _, item := range items {
		network, _ := item.(map[string]any)
		if name, _ := network["name"].(string); name != "" {
			names = append(names, name)
		}
	}

	return names, cobra.ShellCompDirectiveNoFileComp
}

// completeForkyNetworkNames completes the first positional arg with networks
// that have Forky instances.
func completeForkyNetworkNames(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	response, err := runServerOperation(cmd, "forky.list_networks", map[string]any{})
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	data, _ := response.Data.(map[string]any)
	items, _ := data["networks"].([]any)

	names := make([]string, 0, len(items))
	for _, item := range items {
		network, _ := item.(map[string]any)
		if name, _ := network["name"].(string); name != "" {
			names = append(names, name)
		}
	}

	return names, cobra.ShellCompDirectiveNoFileComp
}

// completeEthNodeArgs completes the ethnode positional args. The first arg
// (network) is completed from ethnode's own active networks; the second arg
// (instance) is a per-node DNS label the proxy cannot enumerate, so it is left
// to free text with file completion disabled.
func completeEthNodeArgs(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	response, err := runServerOperation(cmd, "ethnode.list_networks", map[string]any{})
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	data, _ := response.Data.(map[string]any)
	items, _ := data["networks"].([]any)

	names := make([]string, 0, len(items))
	for _, item := range items {
		network, _ := item.(map[string]any)
		if name, _ := network["name"].(string); name != "" {
			names = append(names, name)
		}
	}

	return names, cobra.ShellCompDirectiveNoFileComp
}

// completeSessionIDs completes the first positional arg with session IDs.
func completeSessionIDs(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	response, err := listSessions(commandContext(cmd))
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	ids := make([]string, 0, len(response.Sessions))
	for _, s := range response.Sessions {
		ids = append(ids, s.SessionID)
	}

	return ids, cobra.ShellCompDirectiveNoFileComp
}

// completeSchemaArgs completes the "schema" positional args, narrowing from
// cluster (arg 0) to database (arg 1) to table (arg 2).
func completeSchemaArgs(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	ctx := commandContext(cmd)

	switch len(args) {
	case 0:
		return completeClusterNames(ctx)
	case 1:
		return completeDatabaseNames(ctx, args[0])
	case 2:
		return completeTableNames(ctx, args[0], args[1])
	default:
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
}

func completeClusterNames(ctx context.Context) ([]string, cobra.ShellCompDirective) {
	response, err := listDatasources(ctx, "clickhouse")
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	names := make([]string, 0, len(response.Datasources))
	for _, info := range response.Datasources {
		names = append(names, info.Name)
	}

	return names, cobra.ShellCompDirectiveNoFileComp
}

func completeDatabaseNames(ctx context.Context, cluster string) ([]string, cobra.ShellCompDirective) {
	response, err := readClickHouseClusterTables(ctx, cluster, false)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	seen := make(map[string]struct{})

	var names []string

	for _, c := range response.Clusters {
		if c == nil {
			continue
		}

		for _, database := range c.Databases {
			if database == nil {
				continue
			}

			if _, ok := seen[database.Name]; ok {
				continue
			}

			seen[database.Name] = struct{}{}
			names = append(names, database.Name)
		}

		for _, table := range c.Tables {
			if _, ok := seen[table.Database]; ok {
				continue
			}

			seen[table.Database] = struct{}{}

			names = append(names, table.Database)
		}
	}

	return names, cobra.ShellCompDirectiveNoFileComp
}

func completeTableNames(ctx context.Context, cluster, database string) ([]string, cobra.ShellCompDirective) {
	response, err := readClickHouseDatabaseTables(ctx, cluster, database)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	var names []string

	for _, c := range response.Clusters {
		for _, table := range c.Tables {
			names = append(names, table.Name)
		}
	}

	return names, cobra.ShellCompDirectiveNoFileComp
}

// noCompletions disables file completion for commands with free-text args.
func noCompletions(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	return nil, cobra.ShellCompDirectiveNoFileComp
}
