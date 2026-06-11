package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var (
	promQueryTime     string
	promRangeStart    string
	promRangeEnd      string
	promRangeStep     string
	promLabelStart    string
	promLabelEnd      string
	promLabelContains string
	promLabelLimit    int
)

var prometheusCmd = &cobra.Command{
	GroupID: groupDirect,
	Use:     "prometheus",
	Short:   "Query Prometheus metrics",
	Long: `Query Prometheus for infrastructure metrics.

Prometheus datasource names are deployment-specific. List them before querying.
If you do not know the metric name, discover names through the __name__ label
with --contains and --limit so the output stays small.

Examples:
  panda prometheus list-datasources
  panda prometheus label-values <datasource> __name__ --contains '<term>' --limit 50
  panda prometheus labels <datasource>
  panda prometheus label-values <datasource> job --contains '<term>' --limit 50
  panda prometheus query <datasource> "up"
  panda prometheus query-range <datasource> "rate(http_requests_total[5m])" --start now-1h --end now --step 1m`,
}

func init() {
	rootCmd.AddCommand(prometheusCmd)

	prometheusCmd.AddCommand(promListDatasourcesCmd)
	prometheusCmd.AddCommand(promQueryCmd)
	promQueryCmd.Flags().StringVar(&promQueryTime, "time", "", "Evaluation timestamp (RFC3339, unix, or 'now-1h')")

	prometheusCmd.AddCommand(promQueryRangeCmd)
	promQueryRangeCmd.Flags().StringVar(&promRangeStart, "start", "", "Start time (RFC3339, unix, or 'now-1h'; default 'now-1h')")
	promQueryRangeCmd.Flags().StringVar(&promRangeEnd, "end", "", "End time (RFC3339, unix, or 'now'; default 'now')")
	promQueryRangeCmd.Flags().StringVar(&promRangeStep, "step", "", "Resolution step e.g. '1m' (required)")
	_ = promQueryRangeCmd.MarkFlagRequired("step")

	prometheusCmd.AddCommand(promLabelsCmd)
	promLabelsCmd.Flags().StringVar(&promLabelStart, "start", "", "Start time (RFC3339, unix, or 'now-1h')")
	promLabelsCmd.Flags().StringVar(&promLabelEnd, "end", "", "End time (RFC3339, unix, or 'now')")

	prometheusCmd.AddCommand(promLabelValuesCmd)
	promLabelValuesCmd.Flags().StringVar(&promLabelStart, "start", "", "Start time (RFC3339, unix, or 'now-1h')")
	promLabelValuesCmd.Flags().StringVar(&promLabelEnd, "end", "", "End time (RFC3339, unix, or 'now')")
	promLabelValuesCmd.Flags().StringVar(&promLabelContains, "contains", "", "Case-insensitive substring filter applied before printing")
	promLabelValuesCmd.Flags().IntVar(&promLabelLimit, "limit", 0, "Maximum values to print after filtering (0 = all)")

	promQueryCmd.ValidArgsFunction = completeDatasourceNames("prometheus")
	promQueryRangeCmd.ValidArgsFunction = completeDatasourceNames("prometheus")
	promLabelsCmd.ValidArgsFunction = completeDatasourceNames("prometheus")
	promLabelValuesCmd.ValidArgsFunction = completeDatasourceNames("prometheus")
}

var promListDatasourcesCmd = &cobra.Command{
	Use:   "list-datasources",
	Short: "List available Prometheus datasources",
	RunE: func(cmd *cobra.Command, _ []string) error {
		response, err := runServerOperation(cmd, "prometheus.list_datasources", map[string]any{})
		if err != nil {
			return err
		}

		return printDatasourceList(response)
	},
}

var promQueryCmd = &cobra.Command{
	Use:   "query <datasource> <promql>",
	Short: "Execute an instant PromQL query",
	Long: `Execute an instant PromQL query.

Datasource names come from 'panda prometheus list-datasources'. Metric names
and labels are deployment-specific; discover metric names with:
  panda prometheus label-values <datasource> __name__ --contains '<term>' --limit 50`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		response, err := runServerOperationRaw(cmd, "prometheus.query", map[string]any{
			"datasource": args[0],
			"query":      args[1],
			"time":       promQueryTime,
		})
		if err != nil {
			return err
		}

		if isJSON() {
			return printJSONBytes(response.Body)
		}

		return printPromResult(response.Body)
	},
}

var promQueryRangeCmd = &cobra.Command{
	Use:   "query-range <datasource> <promql>",
	Short: "Execute a range PromQL query",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		response, err := runServerOperationRaw(cmd, "prometheus.query_range", map[string]any{
			"datasource": args[0],
			"query":      args[1],
			"start":      promRangeStart,
			"end":        promRangeEnd,
			"step":       promRangeStep,
		})
		if err != nil {
			return err
		}

		if isJSON() {
			return printJSONBytes(response.Body)
		}

		return printPromResult(response.Body)
	},
}

var promLabelsCmd = &cobra.Command{
	Use:   "labels <datasource>",
	Short: "List all label names",
	Long: `List all label names for a Prometheus datasource.

Datasource names come from 'panda prometheus list-datasources'.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		response, err := runServerOperationRaw(cmd, "prometheus.get_labels", map[string]any{
			"datasource": args[0],
			"start":      promLabelStart,
			"end":        promLabelEnd,
		})
		if err != nil {
			return err
		}

		if isJSON() {
			return printJSONBytes(response.Body)
		}

		return printAPIStringValues(response.Body)
	},
}

var promLabelValuesCmd = &cobra.Command{
	Use:   "label-values <datasource> <label>",
	Short: "Get all values for a label",
	Long: `Get all values for a Prometheus label.

Use label '__name__' to discover metric names. Use --contains and --limit for
broad labels, then query a metric after checking labels with
'panda prometheus labels <datasource>'.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		response, err := runServerOperationRaw(cmd, "prometheus.get_label_values", map[string]any{
			"datasource": args[0],
			"label":      args[1],
			"start":      promLabelStart,
			"end":        promLabelEnd,
		})
		if err != nil {
			return err
		}

		if isJSON() {
			return printJSONBytes(response.Body)
		}

		return printFilteredAPIStringValues(response.Body, promLabelContains, promLabelLimit)
	},
}

func printFilteredAPIStringValues(data []byte, contains string, limit int) error {
	if strings.TrimSpace(contains) == "" && limit <= 0 {
		return printAPIStringValues(data)
	}

	values, err := apiStringValues(data)
	if err != nil {
		return printJSONBytes(data)
	}

	needle := strings.ToLower(strings.TrimSpace(contains))
	printed := 0

	for _, value := range values {
		if needle != "" && !strings.Contains(strings.ToLower(value), needle) {
			continue
		}

		fmt.Println(value)
		printed++

		if limit > 0 && printed >= limit {
			break
		}
	}

	return nil
}

// printPromResult formats a Prometheus API response for human output.
func printPromResult(data []byte) error {
	var resp struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string            `json:"resultType"`
			Result     []json.RawMessage `json:"result"`
		} `json:"data"`
	}

	if err := json.Unmarshal(data, &resp); err != nil {
		return printJSONBytes(data)
	}

	if resp.Status != "success" {
		return printJSONBytes(data)
	}

	for _, r := range resp.Data.Result {
		var entry struct {
			Metric map[string]string `json:"metric"`
			Value  []any             `json:"value"`
			Values [][]any           `json:"values"`
		}

		if err := json.Unmarshal(r, &entry); err != nil {
			fmt.Println(string(r))

			continue
		}

		metric := formatLabelSet(entry.Metric, true)

		if len(entry.Value) == 2 {
			fmt.Printf("%s => %v\n", metric, entry.Value[1])
		} else if entry.Values != nil {
			fmt.Printf("%s:\n", metric)
			for _, v := range entry.Values {
				if len(v) == 2 {
					ts, ok := v[0].(float64)
					if ok {
						fmt.Printf("  %s => %v\n",
							time.Unix(int64(ts), 0).UTC().Format(time.RFC3339), v[1])
					} else {
						fmt.Printf("  %v => %v\n", v[0], v[1])
					}
				}
			}
		}
	}

	return nil
}
