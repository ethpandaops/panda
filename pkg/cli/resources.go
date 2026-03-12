package cli

import (
	"context"
	"fmt"
	"sort"

	"github.com/spf13/cobra"
)

var resourcesCmd = &cobra.Command{
	GroupID: groupDiscovery,
	Use:     "resources",
	Short:   "List and read server resources",
	Long: `List available server resources or read a specific resource by URI.

Resources expose datasource metadata, documentation, and guides that are
also available to MCP-connected clients.

Examples:
  panda resources
  panda resources read ethpandaops://getting-started
  panda resources read python://ethpandaops
  panda resources read clickhouse://tables
  panda resources -o json`,
	RunE: runResourcesList,
}

var resourcesReadCmd = &cobra.Command{
	Use:   "read <uri>",
	Short: "Read a resource by URI",
	Long: `Read a specific resource by its URI and print the content.

Examples:
  panda resources read ethpandaops://getting-started
  panda resources read python://ethpandaops -o json
  panda resources read datasources://clickhouse`,
	Args: cobra.ExactArgs(1),
	RunE: runResourcesRead,
}

func init() {
	rootCmd.AddCommand(resourcesCmd)
	resourcesCmd.AddCommand(resourcesReadCmd)
}

func runResourcesList(_ *cobra.Command, _ []string) error {
	ctx := context.Background()

	response, err := listResources(ctx)
	if err != nil {
		return fmt.Errorf("listing resources: %w", err)
	}

	if isJSON() {
		return printJSON(response)
	}

	sort.Slice(response.Resources, func(i, j int) bool {
		return response.Resources[i].URI < response.Resources[j].URI
	})

	if len(response.Resources) > 0 {
		fmt.Println("Resources:")

		for _, res := range response.Resources {
			desc := res.Description
			if desc == "" {
				desc = res.Name
			}

			fmt.Printf("  %-42s  %s\n", res.URI, desc)
		}
	}

	if len(response.Templates) > 0 {
		sort.Slice(response.Templates, func(i, j int) bool {
			return response.Templates[i].URITemplate < response.Templates[j].URITemplate
		})

		fmt.Println("\nTemplates:")

		for _, tmpl := range response.Templates {
			desc := tmpl.Description
			if desc == "" {
				desc = tmpl.Name
			}

			fmt.Printf("  %-42s  %s\n", tmpl.URITemplate, desc)
		}
	}

	return nil
}

func runResourcesRead(_ *cobra.Command, args []string) error {
	ctx := context.Background()

	response, err := readResource(ctx, args[0])
	if err != nil {
		return fmt.Errorf("reading resource: %w", err)
	}

	if isJSON() {
		return printJSON(response)
	}

	fmt.Print(response.Content)

	return nil
}
