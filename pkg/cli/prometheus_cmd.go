package cli

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/ethpandaops/panda/pkg/operations"
	"github.com/spf13/cobra"
)

var (
	promQueryTime  string
	promRangeStart string
	promRangeEnd   string
	promRangeStep  string
)

var prometheusCmd = &cobra.Command{
	GroupID: groupDirect,
	Use:     "prometheus",
	Short:   "Query Prometheus metrics",
	Long: `Query Prometheus for infrastructure metrics.

Examples:
  panda prometheus list-datasources
  panda prometheus query ethpandaops "up"
  panda prometheus query-range ethpandaops "rate(http_requests_total[5m])" --start now-1h --end now --step 1m
  panda prometheus labels ethpandaops
  panda prometheus label-values ethpandaops job`,
}

func init() {
	rootCmd.AddCommand(prometheusCmd)

	prometheusCmd.AddCommand(promListDatasourcesCmd)
	prometheusCmd.AddCommand(promQueryCmd)
	promQueryCmd.Flags().StringVar(&promQueryTime, "time", "", "Evaluation timestamp (RFC3339, unix, or 'now-1h')")

	prometheusCmd.AddCommand(promQueryRangeCmd)
	promQueryRangeCmd.Flags().StringVar(&promRangeStart, "start", "", "Start time (required)")
	promQueryRangeCmd.Flags().StringVar(&promRangeEnd, "end", "", "End time (required)")
	promQueryRangeCmd.Flags().StringVar(&promRangeStep, "step", "", "Resolution step e.g. '1m' (required)")
	_ = promQueryRangeCmd.MarkFlagRequired("start")
	_ = promQueryRangeCmd.MarkFlagRequired("end")
	_ = promQueryRangeCmd.MarkFlagRequired("step")

	prometheusCmd.AddCommand(promLabelsCmd)
	prometheusCmd.AddCommand(promLabelValuesCmd)

	promQueryCmd.ValidArgsFunction = completeDatasourceNames("prometheus")
	promQueryRangeCmd.ValidArgsFunction = completeDatasourceNames("prometheus")
	promLabelsCmd.ValidArgsFunction = completeDatasourceNames("prometheus")
	promLabelValuesCmd.ValidArgsFunction = completeDatasourceNames("prometheus")
}

var promListDatasourcesCmd = &cobra.Command{
	Use:   "list-datasources",
	Short: "List available Prometheus datasources",
	RunE: func(_ *cobra.Command, _ []string) error {
		items, err := listPrometheusDatasources()
		if err != nil {
			return err
		}

		if prometheusJSON || isJSON() {
			return printJSON(operations.DatasourcesPayload{Datasources: items})
		}

		if len(items) == 0 {
			fmt.Println("No Prometheus datasources found.")
			return nil
		}

		for _, item := range items {
			name := item.Name
			desc := item.Description
			targetURL := item.URL

			if targetURL != "" {
				fmt.Printf("  %-16s  %-24s  %s\n", name, desc, targetURL)
				continue
			}

			fmt.Printf("  %-16s  %s\n", name, desc)
		}

		return nil
	},
}

var promQueryCmd = &cobra.Command{
	Use:   "query <datasource> <promql>",
	Short: "Execute an instant PromQL query",
	Args:  cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		response, err := prometheusQuery(operations.PrometheusQueryArgs{
			Datasource: args[0],
			Query:      args[1],
			Time:       promQueryTime,
		})
		if err != nil {
			return err
		}

		if prometheusJSON || isJSON() {
			return printJSONBytes(response.Body)
		}

		return printPromResult(response.Body)
	},
}

var promQueryRangeCmd = &cobra.Command{
	Use:   "query-range <datasource> <promql>",
	Short: "Execute a range PromQL query",
	Args:  cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		response, err := prometheusQueryRange(operations.PrometheusRangeQueryArgs{
			Datasource: args[0],
			Query:      args[1],
			Start:      promRangeStart,
			End:        promRangeEnd,
			Step:       promRangeStep,
		})
		if err != nil {
			return err
		}

		if prometheusJSON || isJSON() {
			return printJSONBytes(response.Body)
		}

		return printPromResult(response.Body)
	},
}

var promLabelsCmd = &cobra.Command{
	Use:   "labels <datasource>",
	Short: "List all label names",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		response, err := prometheusLabels(operations.DatasourceArgs{Datasource: args[0]})
		if err != nil {
			return err
		}

		if prometheusJSON || isJSON() {
			return printJSONBytes(response.Body)
		}

		return printAPIStringValues(response.Body)
	},
}

var promLabelValuesCmd = &cobra.Command{
	Use:   "label-values <datasource> <label>",
	Short: "Get all values for a label",
	Args:  cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		response, err := prometheusLabelValues(operations.DatasourceLabelArgs{
			Datasource: args[0],
			Label:      args[1],
		})
		if err != nil {
			return err
		}

		if prometheusJSON || isJSON() {
			return printJSONBytes(response.Body)
		}

		return printAPIStringValues(response.Body)
	},
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
