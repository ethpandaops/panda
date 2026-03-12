package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/ethpandaops/panda/pkg/operations"
)

var (
	outputFormat string
	outputJSON   bool
)

// isJSON returns true if the output format is JSON.
func isJSON() bool {
	return outputFormat == "json"
}

// printJSON marshals v as indented JSON and prints it to stdout.
func printJSON(v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling JSON: %w", err)
	}

	fmt.Println(string(data))

	return nil
}

// printJSONBytes parses raw JSON bytes and pretty-prints them.
func printJSONBytes(data []byte) error {
	var payload any
	if err := json.Unmarshal(data, &payload); err != nil {
		fmt.Println(string(data))

		return nil
	}

	return printJSON(payload)
}

// printTable renders rows as an aligned table with optional headers.
// Pass nil headers to print rows without a header line.
func printTable(headers []string, rows [][]string) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

	if len(headers) > 0 {
		_, _ = fmt.Fprintln(w, strings.Join(headers, "\t"))
	}

	for _, row := range rows {
		_, _ = fmt.Fprintln(w, strings.Join(row, "\t"))
	}

	_ = w.Flush()
}

// printKeyValue renders key-value pairs with aligned keys.
func printKeyValue(pairs [][2]string) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

	for _, pair := range pairs {
		_, _ = fmt.Fprintf(w, "%s:\t%s\n", pair[0], pair[1])
	}

	_ = w.Flush()
}

// printDatasourceList renders a list of datasources from an operations response.
// This is shared across clickhouse, prometheus, and loki list-datasources commands.
func printDatasourceList(response *operations.Response) error {
	if isJSON() {
		return printJSON(response)
	}

	data, _ := response.Data.(map[string]any)
	items, _ := data["datasources"].([]any)

	if len(items) == 0 {
		fmt.Println("No datasources found.")

		return nil
	}

	rows := make([][]string, 0, len(items))

	for _, item := range items {
		ds, _ := item.(map[string]any)
		name, _ := ds["name"].(string)
		desc, _ := ds["description"].(string)

		if desc == "" {
			desc = name
		}

		rows = append(rows, []string{name, desc})
	}

	sort.Slice(rows, func(i, j int) bool {
		return rows[i][0] < rows[j][0]
	})

	printTable([]string{"NAME", "DESCRIPTION"}, rows)

	return nil
}
