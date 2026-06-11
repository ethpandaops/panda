package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ethpandaops/panda/pkg/serverapi"
	"github.com/ethpandaops/panda/pkg/types"
)

var docsCmd = &cobra.Command{
	GroupID: groupDiscovery,
	Use:     "docs [module-name]",
	Short:   "Show Python API documentation",
	Long: `Show documentation for the ethpandaops Python library modules available
in the sandbox. Without arguments, lists all modules. With a module name,
shows detailed function signatures and descriptions.

Examples:
  panda docs                  # List all modules
  panda docs clickhouse       # Show clickhouse module docs
  panda docs clickhouse.query # Show one function
  panda docs --json           # Output as JSON`,
	Args: cobra.MaximumNArgs(1),
	RunE: runDocs,
}

func init() {
	rootCmd.AddCommand(docsCmd)
	docsCmd.ValidArgsFunction = completeDocsModules
}

// completeDocsModules completes the docs positional arg with the live module
// set provided by the server, keeping completion in sync with what 'panda docs'
// actually accepts.
func completeDocsModules(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	allDocs, err := getAllPythonAPIDocs(commandContext(cmd))
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	names := make([]string, 0, len(allDocs))
	for name := range allDocs {
		names = append(names, name)
	}

	return names, cobra.ShellCompDirectiveNoFileComp
}

func runDocs(cmd *cobra.Command, args []string) error {
	allDocs, err := getAllPythonAPIDocs(cmd.Context())
	if err != nil {
		return err
	}

	if isJSON() {
		if len(args) > 0 {
			if moduleName, functionName, ok := splitDocFunction(args[0]); ok {
				doc, found := findFunctionDoc(allDocs, moduleName, functionName)
				if !found {
					return fmt.Errorf("function %q not found", args[0])
				}

				return printJSON(map[string]any{
					moduleName: map[string]any{
						functionName: doc,
					},
				})
			}

			doc, ok := allDocs[args[0]]
			if !ok {
				return docsModuleNotFoundError(allDocs, args[0])
			}

			return printJSON(map[string]any{args[0]: doc})
		}

		return printJSON(allDocs)
	}

	if len(args) == 0 {
		return listModules(allDocs)
	}

	if moduleName, functionName, ok := splitDocFunction(args[0]); ok {
		return showFunction(allDocs, moduleName, functionName)
	}

	return showModule(allDocs, args[0])
}

func splitDocFunction(value string) (string, string, bool) {
	moduleName, functionName, ok := strings.Cut(value, ".")
	if !ok || moduleName == "" || functionName == "" || strings.Contains(functionName, ".") {
		return "", "", false
	}

	return moduleName, functionName, true
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
		return docsModuleNotFoundError(docs, name)
	}

	fmt.Printf("Module: %s\n%s\n\n", name, doc.Description)

	funcNames := make([]string, 0, len(doc.Functions))
	for fn := range doc.Functions {
		funcNames = append(funcNames, fn)
	}

	sort.Strings(funcNames)

	for _, fn := range funcNames {
		printFunctionDoc(doc.Functions[fn])
	}

	return nil
}

func docsModuleNotFoundError(docs map[string]types.ModuleDoc, name string) error {
	var b strings.Builder

	fmt.Fprintf(&b, "module %q not found in Python API docs", name)
	if isRootCommandName(name) {
		fmt.Fprintf(&b, "; for CLI command help use 'panda %s --help'", name)
	}

	names := make([]string, 0, len(docs))
	for moduleName := range docs {
		names = append(names, moduleName)
	}
	sort.Strings(names)
	if len(names) > 0 {
		fmt.Fprintf(&b, ". Available Python modules: %s", strings.Join(names, ", "))
	}

	return fmt.Errorf("%s", b.String())
}

func isRootCommandName(name string) bool {
	for _, cmd := range rootCmd.Commands() {
		if cmd.Name() == name {
			return true
		}

		for _, alias := range cmd.Aliases {
			if alias == name {
				return true
			}
		}
	}

	return false
}

func showFunction(docs map[string]types.ModuleDoc, moduleName, functionName string) error {
	fd, ok := findFunctionDoc(docs, moduleName, functionName)
	if !ok {
		return fmt.Errorf("function %q not found", moduleName+"."+functionName)
	}

	fmt.Printf("Function: %s.%s\n\n", moduleName, functionName)
	printFunctionDoc(fd)

	return nil
}

func findFunctionDoc(docs map[string]types.ModuleDoc, moduleName, functionName string) (types.FunctionDoc, bool) {
	doc, ok := docs[moduleName]
	if !ok {
		return types.FunctionDoc{}, false
	}

	fd, ok := doc.Functions[functionName]

	return fd, ok
}

func printFunctionDoc(fd types.FunctionDoc) {
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

func getAllPythonAPIDocs(ctx context.Context) (map[string]types.ModuleDoc, error) {
	response, err := readResource(ctx, "python://ethpandaops")
	if err != nil {
		return nil, err
	}

	var payload serverapi.APIDocResponse
	if err := json.Unmarshal([]byte(response.Content), &payload); err != nil {
		return nil, fmt.Errorf("decoding API docs: %w", err)
	}

	return payload.Modules, nil
}
