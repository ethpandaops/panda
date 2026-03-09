package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
)

var (
	prometheusJSON bool
	promQueryTime  string
	promRangeStart string
	promRangeEnd   string
	promRangeStep  string
)

var prometheusCmd = &cobra.Command{
	Use:   "prometheus",
	Short: "Query Prometheus metrics",
	Long: `Query Prometheus for infrastructure metrics.

Examples:
  ep prometheus query ethpandaops "up"
  ep prometheus query-range ethpandaops "rate(http_requests_total[5m])" --start now-1h --end now --step 1m
  ep prometheus labels ethpandaops
  ep prometheus label-values ethpandaops job`,
}

func init() {
	rootCmd.AddCommand(prometheusCmd)
	prometheusCmd.PersistentFlags().BoolVar(&prometheusJSON, "json", false, "Output in JSON format")

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
}

var promQueryCmd = &cobra.Command{
	Use:   "query <datasource> <promql>",
	Short: "Execute an instant PromQL query",
	Args:  cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		ctx := context.Background()

		pc, cleanup, err := startProxy(ctx)
		if err != nil {
			return err
		}

		defer cleanup()

		params := url.Values{"query": {args[1]}}
		if promQueryTime != "" {
			params.Set("time", promQueryTime)
		}

		data, err := proxyGet(ctx, pc, "/prometheus/api/v1/query", params, dsHeader(args[0]))
		if err != nil {
			return err
		}

		if prometheusJSON {
			return printJSONBytes(data)
		}

		return printPromResult(data)
	},
}

var promQueryRangeCmd = &cobra.Command{
	Use:   "query-range <datasource> <promql>",
	Short: "Execute a range PromQL query",
	Args:  cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		ctx := context.Background()

		pc, cleanup, err := startProxy(ctx)
		if err != nil {
			return err
		}

		defer cleanup()

		params := url.Values{
			"query": {args[1]},
			"start": {promRangeStart},
			"end":   {promRangeEnd},
			"step":  {promRangeStep},
		}

		data, err := proxyGet(ctx, pc, "/prometheus/api/v1/query_range", params, dsHeader(args[0]))
		if err != nil {
			return err
		}

		if prometheusJSON {
			return printJSONBytes(data)
		}

		return printPromResult(data)
	},
}

var promLabelsCmd = &cobra.Command{
	Use:   "labels <datasource>",
	Short: "List all label names",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		ctx := context.Background()

		pc, cleanup, err := startProxy(ctx)
		if err != nil {
			return err
		}

		defer cleanup()

		data, err := proxyGet(ctx, pc, "/prometheus/api/v1/labels", nil, dsHeader(args[0]))
		if err != nil {
			return err
		}

		if prometheusJSON {
			return printJSONBytes(data)
		}

		var resp struct {
			Data []string `json:"data"`
		}

		if err := json.Unmarshal(data, &resp); err != nil {
			return printJSONBytes(data)
		}

		for _, label := range resp.Data {
			fmt.Println(label)
		}

		return nil
	},
}

var promLabelValuesCmd = &cobra.Command{
	Use:   "label-values <datasource> <label>",
	Short: "Get all values for a label",
	Args:  cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		ctx := context.Background()

		pc, cleanup, err := startProxy(ctx)
		if err != nil {
			return err
		}

		defer cleanup()

		path := fmt.Sprintf("/prometheus/api/v1/label/%s/values", args[1])

		data, err := proxyGet(ctx, pc, path, nil, dsHeader(args[0]))
		if err != nil {
			return err
		}

		if prometheusJSON {
			return printJSONBytes(data)
		}

		var resp struct {
			Data []string `json:"data"`
		}

		if err := json.Unmarshal(data, &resp); err != nil {
			return printJSONBytes(data)
		}

		for _, val := range resp.Data {
			fmt.Println(val)
		}

		return nil
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

		// Format metric labels.
		var labels []string
		for k, v := range entry.Metric {
			labels = append(labels, fmt.Sprintf("%s=%q", k, v))
		}

		metric := "{" + strings.Join(labels, ", ") + "}"

		if len(entry.Value) == 2 {
			fmt.Printf("%s => %v\n", metric, entry.Value[1])
		} else if entry.Values != nil {
			fmt.Printf("%s:\n", metric)
			for _, v := range entry.Values {
				if len(v) == 2 {
					fmt.Printf("  %v => %v\n", v[0], v[1])
				}
			}
		}
	}

	return nil
}
