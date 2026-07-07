package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// computeColumn maps a table header to a dotted field path, with an optional
// value formatter.
type computeColumn struct {
	header string
	path   string
	format func(any) string
}

// computeColumnsByOperation curates table columns for the well-known list-shaped
// operations. Operations without an entry fall back to inferred columns.
var computeColumnsByOperation = map[string][]computeColumn{
	"compute.list_sandboxes":           sandboxColumns,
	"compute.get_snapshot_restored_by": sandboxColumns,
	"compute.list_snapshots":           snapshotColumns,
	"compute.get_sandbox_snapshots":    snapshotColumns,
	"compute.list_templates":           templateColumns,
	"compute.list_operations":          operationColumns,
	"compute.get_sandbox_operations":   operationColumns,
	"compute.list_ssh_keys":            sshKeyColumns,
	"compute.list_nodes":               nodeColumns,
	"compute.list_users":               userColumns,
	"compute.list_forks":               forkColumns,
}

var sandboxColumns = []computeColumn{
	{header: "ID", path: "id"},
	{header: "STATE", path: "state"},
	{header: "TEMPLATE", path: "template"},
	{header: "VER", path: "ver"},
	{header: "NODE", path: "node"},
	{header: "CREATED", path: "createdAt", format: formatComputeTime},
	{header: "EXPIRES", path: "expiresAt", format: formatComputeTime},
}

var snapshotColumns = []computeColumn{
	{header: "ID", path: "id"},
	{header: "STATE", path: "state"},
	{header: "SANDBOX", path: "sandboxId"},
	{header: "TEMPLATE", path: "template"},
	{header: "VER", path: "ver"},
	{header: "CREATED", path: "createdAt", format: formatComputeTime},
	{header: "EXPIRES", path: "expiresAt", format: formatComputeTime},
}

var templateColumns = []computeColumn{
	{header: "NAME", path: "name"},
	{header: "VERSION", path: "ver"},
	{header: "SIZING", path: "sizing"},
	{header: "CLOCK", path: "clockPolicy"},
	{header: "PINNED", path: "pinned"},
}

var forkColumns = []computeColumn{
	{header: "ID", path: "id"},
	{header: "STATE", path: "state"},
	{header: "SOURCE", path: "source.kind"},
	{header: "SOURCE-ID", path: "source.id"},
	{header: "REQUESTED", path: "requested"},
	{header: "RUNNING", path: "running"},
	{header: "QUEUED", path: "queued"},
	{header: "FAILED", path: "failed"},
	{header: "CREATED", path: "created_at", format: formatComputeTime},
}

var operationColumns = []computeColumn{
	{header: "ID", path: "id"},
	{header: "TYPE", path: "type"},
	{header: "STATE", path: "state"},
	{header: "TARGET", path: "target"},
	{header: "STARTED", path: "startedAt", format: formatComputeTime},
}

var nodeColumns = []computeColumn{
	{header: "ID", path: "id"},
	{header: "STATUS", path: "status"},
	{header: "ZONE", path: "zone"},
	{header: "OS", path: "os"},
	{header: "VCPU", path: "vcpuTotal"},
	{header: "MEM", path: "memTotal"},
}

var userColumns = []computeColumn{
	{header: "HANDLE", path: "handle"},
	{header: "NAME", path: "name"},
	{header: "EMAIL", path: "email"},
	{header: "TYPE", path: "type"},
}

// sshKeyColumns renders the caller's own keys (/v1/me/ssh-keys), which keep the
// snake_case SSHPublicKey shape.
var sshKeyColumns = []computeColumn{
	{header: "ID", path: "id"},
	{header: "NAME", path: "name"},
	{header: "FINGERPRINT", path: "fingerprint"},
	{header: "CREATED", path: "created_at", format: formatComputeTime},
}

// renderComputeRaw renders a compute response. JSON output is emitted verbatim
// (number precision preserved) when --output json is set; otherwise the response
// is rendered as a table for list shapes or key-value pairs for a single object.
func renderComputeRaw(operationID string, body []byte) error {
	if isJSON() {
		return printJSONBytes(body)
	}

	decoded, err := decodeComputeBody(body)
	if err != nil {
		// Not JSON we can shape (e.g. plain-text logs); fall back to raw.
		return printJSONBytes(body)
	}

	items, ok := computeListItems(decoded)
	if !ok {
		return renderComputeObject(decoded)
	}

	return renderComputeList(operationID, items)
}

// decodeComputeBody decodes a JSON body, preserving numeric literals.
func decodeComputeBody(body []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()

	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, err
	}

	return decoded, nil
}

// computeListItems extracts list items from a decoded response. Both the
// {"items": [...]} envelope and a bare top-level array are recognised.
func computeListItems(decoded any) ([]map[string]any, bool) {
	switch shaped := decoded.(type) {
	case map[string]any:
		raw, ok := shaped["items"].([]any)
		if !ok {
			return nil, false
		}

		return toMapSlice(raw), true
	case []any:
		return toMapSlice(shaped), true
	default:
		return nil, false
	}
}

// toMapSlice keeps only the object elements of a decoded array.
func toMapSlice(raw []any) []map[string]any {
	items := make([]map[string]any, 0, len(raw))

	for _, element := range raw {
		if obj, ok := element.(map[string]any); ok {
			items = append(items, obj)
		}
	}

	return items
}

// renderComputeList prints list items as an aligned table.
func renderComputeList(operationID string, items []map[string]any) error {
	if len(items) == 0 {
		fmt.Println("No results found.")

		return nil
	}

	columns := computeColumnsByOperation[operationID]
	if columns == nil {
		columns = inferComputeColumns(items)
	}

	headers := make([]string, 0, len(columns))
	for _, column := range columns {
		headers = append(headers, column.header)
	}

	rows := make([][]string, 0, len(items))

	for _, item := range items {
		row := make([]string, 0, len(columns))

		for _, column := range columns {
			row = append(row, formatComputeCell(item, column))
		}

		rows = append(rows, row)
	}

	printTable(headers, rows)

	if len(items) == 1 {
		fmt.Println("\n1 result.")
	} else {
		fmt.Printf("\n%d results.\n", len(items))
	}

	return nil
}

// renderComputeObject prints a single object as aligned key-value pairs, with
// scalars first and nested structures rendered as compact JSON.
func renderComputeObject(decoded any) error {
	obj, ok := decoded.(map[string]any)
	if !ok {
		return printJSON(decoded)
	}

	if opID, hasOp := obj["op_id"].(string); hasOp {
		return renderComputeAccepted(obj, opID)
	}

	keys := make([]string, 0, len(obj))
	for key := range obj {
		keys = append(keys, key)
	}

	sort.Slice(keys, func(i, j int) bool {
		return computeKeyRank(keys[i], keys[j])
	})

	pairs := make([][2]string, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, [2]string{key, formatComputeValue(obj[key])})
	}

	printKeyValue(pairs)

	return nil
}

// renderComputeAccepted prints a friendly summary for async accepted responses
// and points at the operations poll command.
func renderComputeAccepted(obj map[string]any, opID string) error {
	pairs := make([][2]string, 0, 4)
	pairs = append(pairs, [2]string{"operation", opID})

	for _, key := range []string{"id", "sandbox_id", "snapshot_id", "fork_id"} {
		if value, ok := obj[key].(string); ok && value != "" {
			pairs = append(pairs, [2]string{key, value})
		}
	}

	fmt.Println("Accepted.")
	printKeyValue(pairs)
	fmt.Printf("\nPoll with: panda compute operations get %s\n", opID)

	return nil
}

// formatComputeCell formats one table cell from an item field.
func formatComputeCell(item map[string]any, column computeColumn) string {
	value, ok := lookupPath(item, column.path)
	if !ok || value == nil {
		return "-"
	}

	if column.format != nil {
		return column.format(value)
	}

	text := stringifyComputeScalar(value)
	if text == "" {
		return "-"
	}

	return text
}

// formatComputeValue renders a key-value field: scalars as text, nested
// structures as compact JSON.
func formatComputeValue(value any) string {
	switch value.(type) {
	case map[string]any, []any:
		encoded, err := json.Marshal(value)
		if err != nil {
			return fmt.Sprint(value)
		}

		return string(encoded)
	default:
		text := stringifyComputeScalar(value)
		if text == "" {
			return "-"
		}

		return text
	}
}

// inferComputeColumns derives table columns from the scalar fields present
// across items, with id/name surfaced first and the rest sorted.
func inferComputeColumns(items []map[string]any) []computeColumn {
	seen := make(map[string]struct{}, 16)

	for _, item := range items {
		for key, value := range item {
			if isScalar(value) {
				seen[key] = struct{}{}
			}
		}
	}

	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}

	sort.Slice(keys, func(i, j int) bool {
		return computeKeyRank(keys[i], keys[j])
	})

	columns := make([]computeColumn, 0, len(keys))
	for _, key := range keys {
		columns = append(columns, computeColumn{
			header: strings.ToUpper(key),
			path:   key,
			format: timestampFormatter(key),
		})
	}

	return columns
}

// lookupPath resolves a dotted field path against a decoded object.
func lookupPath(item map[string]any, path string) (any, bool) {
	parts := strings.Split(path, ".")

	var current any = item

	for _, part := range parts {
		obj, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}

		current, ok = obj[part]
		if !ok {
			return nil, false
		}
	}

	return current, true
}

// stringifyComputeScalar renders a scalar JSON value as text. Non-scalars yield
// an empty string.
func stringifyComputeScalar(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	default:
		return ""
	}
}

func isScalar(value any) bool {
	switch value.(type) {
	case map[string]any, []any:
		return false
	default:
		return true
	}
}

// computeKeyRank orders object keys: id, then name, then the rest sorted.
func computeKeyRank(a, b string) bool {
	ra, rb := keyPriority(a), keyPriority(b)
	if ra != rb {
		return ra < rb
	}

	return a < b
}

func keyPriority(key string) int {
	switch key {
	case "id":
		return 0
	case "name":
		return 1
	default:
		return 2
	}
}

func timestampFormatter(key string) func(any) string {
	// Match both snake_case (created_at) and camelCase (createdAt) timestamp keys.
	if strings.HasSuffix(key, "_at") || strings.HasSuffix(key, "_time") ||
		strings.HasSuffix(key, "At") || strings.HasSuffix(key, "Time") {
		return formatComputeTime
	}

	return nil
}

// formatComputeTime renders an RFC3339 timestamp as a relative duration (e.g.
// "5m ago", "in 1h"). Non-timestamps are returned unchanged.
func formatComputeTime(value any) string {
	text := stringifyComputeScalar(value)
	if text == "" {
		return "-"
	}

	parsed, err := time.Parse(time.RFC3339, text)
	if err != nil {
		return text
	}

	delta := time.Since(parsed)

	switch {
	case delta >= 0 && delta < time.Minute:
		return "just now"
	case delta >= time.Minute:
		return humanizeDuration(delta) + " ago"
	default:
		return "in " + humanizeDuration(-delta)
	}
}

// humanizeDuration renders a positive duration at its largest natural unit.
func humanizeDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
