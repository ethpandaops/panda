package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/ethpandaops/panda/pkg/operations"
)

var outputFormat string

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

// printJSONBytes pretty-prints raw JSON bytes, preserving original number
// precision (avoids float64 round-trip that loses large integers).
func printJSONBytes(data []byte) error {
	var buf bytes.Buffer
	if err := json.Indent(&buf, data, "", "  "); err != nil {
		fmt.Println(string(data))

		return nil
	}

	buf.WriteByte('\n')
	_, err := buf.WriteTo(os.Stdout)

	return err
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

// printAPIStringValues parses a JSON response with a "data" array of strings
// and prints each value on its own line.
func printAPIStringValues(data []byte) error {
	var resp struct {
		Data []any `json:"data"`
	}

	if err := json.Unmarshal(data, &resp); err != nil {
		return printJSONBytes(data)
	}

	for _, value := range resp.Data {
		fmt.Println(value)
	}

	return nil
}

// formatLabelSet sorts label keys and formats them as {key=value, ...}.
// If quoteValues is true, values are quoted (Prometheus style); otherwise bare (Loki style).
func formatLabelSet(labels map[string]string, quoteValues bool) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	parts := make([]string, 0, len(keys))

	for _, k := range keys {
		if quoteValues {
			parts = append(parts, fmt.Sprintf("%s=%q", k, labels[k]))
		} else {
			parts = append(parts, fmt.Sprintf("%s=%s", k, labels[k]))
		}
	}

	return "{" + strings.Join(parts, ", ") + "}"
}

// nestedMap extracts a nested map from an any value. If key is empty, it
// type-asserts value directly; otherwise it traverses one level into the map.
func nestedMap(value any, key string) map[string]any {
	if key == "" {
		data, _ := value.(map[string]any)
		return data
	}

	data, _ := value.(map[string]any)
	nested, _ := data[key].(map[string]any)

	return nested
}

// intFromAny coerces a numeric any value to int64.
func intFromAny(value any) int64 {
	switch typed := value.(type) {
	case float64:
		return int64(typed)
	case int:
		return int64(typed)
	case int64:
		return typed
	default:
		return 0
	}
}
