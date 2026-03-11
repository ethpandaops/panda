package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ethpandaops/panda/pkg/resource"
	"github.com/ethpandaops/panda/pkg/types"
)

var docsJSON bool

var docsCmd = &cobra.Command{
	Use:   "docs [module-name]",
	Short: "Show Python API documentation",
	Long: `Show documentation for the ethpandaops Python library modules available
in the sandbox. Without arguments, lists all modules. With a module name,
shows detailed function signatures and descriptions.

Examples:
  panda docs                  # List all modules
  panda docs clickhouse       # Show clickhouse module docs
  panda docs --json           # Output as JSON`,
	RunE:      runDocs,
	ValidArgs: []string{"clickhouse", "prometheus", "loki", "dora", "storage", "ethnode"},
}

func init() {
	rootCmd.AddCommand(docsCmd)
	docsCmd.Flags().BoolVar(&docsJSON, "json", false, "Output in JSON format")
}

func runDocs(_ *cobra.Command, args []string) error {
	allDocs, err := getAllPythonAPIDocs(context.Background())
	if err != nil {
		return err
	}

	if docsJSON {
		if len(args) > 0 {
			doc, ok := allDocs[args[0]]
			if !ok {
				return fmt.Errorf("module %q not found", args[0])
			}

			return printJSON(map[string]any{args[0]: doc})
		}

		return printJSON(allDocs)
	}

	if len(args) == 0 {
		return listModules(allDocs)
	}

	return showModule(allDocs, args[0])
}

func listModules(docs map[string]types.ModuleDoc) error {
	names := make([]string, 0, len(docs))
	for name := range docs {
		names = append(names, name)
	}

	sort.Strings(names)

	fmt.Println("Available modules:")

	for _, name := range names {
		doc := docs[name]
		fmt.Printf("  %-16s  %s\n", name, doc.Description)
	}

	fmt.Println("\nUse 'panda docs <module>' for detailed function documentation.")

	return nil
}

func showModule(docs map[string]types.ModuleDoc, name string) error {
	doc, ok := docs[name]
	if !ok {
		return fmt.Errorf("module %q not found", name)
	}

	fmt.Printf("Module: %s\n%s\n\n", name, doc.Description)

	funcNames := make([]string, 0, len(doc.Functions))
	for fn := range doc.Functions {
		funcNames = append(funcNames, fn)
	}

	sort.Strings(funcNames)

	for _, fn := range funcNames {
		fd := doc.Functions[fn]
		fmt.Printf("  %s\n", fd.Signature)
		fmt.Printf("    %s\n", fd.Description)

		if fd.Returns != "" {
			fmt.Printf("    Returns: %s\n", fd.Returns)
		}

		if len(fd.Parameters) > 0 {
			paramNames := make([]string, 0, len(fd.Parameters))
			for p := range fd.Parameters {
				paramNames = append(paramNames, p)
			}

			sort.Strings(paramNames)

			fmt.Println("    Parameters:")

			for _, p := range paramNames {
				fmt.Printf("      %-12s  %s\n", p, fd.Parameters[p])
			}
		}

		if fd.Example != "" {
			fmt.Printf("    Example: %s\n", strings.TrimSpace(fd.Example))
		}

		fmt.Println()
	}

	return nil
}

func getAllPythonAPIDocs(ctx context.Context) (map[string]types.ModuleDoc, error) {
	response, err := readResource(ctx, "python://ethpandaops")
	if err != nil {
		return nil, err
	}

	var payload resource.APIDocResponse
	if err := json.Unmarshal([]byte(response.Content), &payload); err != nil {
		return nil, fmt.Errorf("decoding API docs: %w", err)
	}

	return payload.Modules, nil
}
