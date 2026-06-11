package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"

	"github.com/spf13/cobra"

	"github.com/ethpandaops/panda/pkg/serverapi"
	"github.com/ethpandaops/panda/pkg/surface"
)

var resourcesCmd = &cobra.Command{
	GroupID: groupDiscovery,
	Use:     "resources [uri]",
	Short:   "List and read server resources",
	Long: `List available server resources or read a specific resource by URI.

Resources expose datasource metadata, documentation, and guides that are
also available to MCP-connected clients.

Examples:
  panda resources
  panda resources panda://getting-started
  panda resources datasets://list
  panda resources read panda://getting-started
  panda resources read networks://active
  panda resources read python://ethpandaops
  panda resources read clickhouse://tables/<cluster>/<database>
  panda resources -o json`,
	Args: cobra.MaximumNArgs(1),
	RunE: runResources,
}

var resourcesReadCmd = &cobra.Command{
	Use:     "read <uri>",
	Aliases: []string{"get"},
	Short:   "Read a resource by URI",
	Long: `Read a specific resource by its URI and print the content.

Examples:
  panda resources read panda://getting-started
  panda resources read networks://active
  panda resources read python://ethpandaops -o json
  panda resources read datasources://clickhouse`,
	Args: cobra.ExactArgs(1),
	RunE: runResourcesRead,
}

var resourcesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List available server resources",
	Args:  cobra.NoArgs,
	RunE:  runResourcesList,
}

func init() {
	rootCmd.AddCommand(resourcesCmd)
	resourcesCmd.AddCommand(resourcesListCmd)
	resourcesCmd.AddCommand(resourcesReadCmd)
}

func runResources(cmd *cobra.Command, args []string) error {
	if len(args) == 1 {
		return runResourcesRead(cmd, args)
	}

	return runResourcesList(cmd, args)
}

func runResourcesList(cmd *cobra.Command, _ []string) error {
	response, err := listResources(cmd.Context())
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

	fmt.Println("\nRead a resource: panda resources <uri>")

	return nil
}

func runResourcesRead(cmd *cobra.Command, args []string) error {
	response, err := readResource(cmd.Context(), args[0])
	if err != nil {
		return fmt.Errorf("reading resource: %w", err)
	}

	if isJSON() {
		return printJSON(response)
	}

	fmt.Print(response.Content)

	return nil
}

func listResources(ctx context.Context) (*serverapi.ListResourcesResponse, error) {
	var response serverapi.ListResourcesResponse
	if err := serverGetJSON(ctx, "/api/v1/resources", nil, &response); err != nil {
		return nil, err
	}

	return &response, nil
}

func readResource(ctx context.Context, uri string) (*serverapi.ResourceResponse, error) {
	query := url.Values{"uri": []string{uri}}
	query.Set(surface.QueryParam, surface.CLI.Key())

	data, status, headers, err := serverDo(ctx, http.MethodGet, "/api/v1/resources/read", nil, query, nil)
	if err != nil {
		return nil, err
	}

	if status < 200 || status >= 300 {
		return nil, decodeAPIError(status, data)
	}

	return &serverapi.ResourceResponse{
		URI:      uri,
		MIMEType: headers.Get("Content-Type"),
		Content:  string(data),
	}, nil
}
