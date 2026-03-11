package cli

import (
	"context"

	"github.com/spf13/cobra"
)

// completeDatasourceNames completes the first positional arg with datasource names.
func completeDatasourceNames(dsType string) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		response, err := listDatasources(context.Background(), dsType)
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
func completeNetworkNames(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	networks, err := listDoraNetworks()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	names := make([]string, 0, len(networks))
	for _, network := range networks {
		if network.Name != "" {
			names = append(names, network.Name)
		}
	}

	return names, cobra.ShellCompDirectiveNoFileComp
}

// completeSessionIDs completes the first positional arg with session IDs.
func completeSessionIDs(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	response, err := listSessions(context.Background())
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	ids := make([]string, 0, len(response.Sessions))
	for _, s := range response.Sessions {
		ids = append(ids, s.SessionID)
	}

	return ids, cobra.ShellCompDirectiveNoFileComp
}

// completeTableNames completes the first positional arg with ClickHouse table names.
func completeTableNames(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	response, err := readClickHouseTables(context.Background())
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	var names []string
	for _, cluster := range response.Clusters {
		for _, table := range cluster.Tables {
			names = append(names, table.Name)
		}
	}

	return names, cobra.ShellCompDirectiveNoFileComp
}

// noCompletions disables file completion for commands with free-text args.
func noCompletions(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	return nil, cobra.ShellCompDirectiveNoFileComp
}
