package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

	"github.com/spf13/cobra"
)

var (
	lokiJSON      bool
	lokiLimit     int
	lokiStart     string
	lokiEnd       string
	lokiDirection string
)

var lokiCmd = &cobra.Command{
	Use:   "loki",
	Short: "Query Loki logs",
	Long: `Query Loki for log data.

Examples:
  ep loki query ethpandaops '{app="beacon-node"}'
  ep loki query ethpandaops '{app="beacon-node"} |= "error"' --limit 50 --start now-1h
  ep loki labels ethpandaops
  ep loki label-values ethpandaops app`,
}

func init() {
	rootCmd.AddCommand(lokiCmd)
	lokiCmd.PersistentFlags().BoolVar(&lokiJSON, "json", false, "Output in JSON format")

	lokiCmd.AddCommand(lokiQueryCmd)
	lokiQueryCmd.Flags().IntVar(&lokiLimit, "limit", 100, "Maximum entries to return")
	lokiQueryCmd.Flags().StringVar(&lokiStart, "start", "", "Start time (RFC3339, unix, or 'now-1h')")
	lokiQueryCmd.Flags().StringVar(&lokiEnd, "end", "", "End time (RFC3339, unix, or 'now')")
	lokiQueryCmd.Flags().StringVar(&lokiDirection, "direction", "backward", "Sort direction: forward or backward")

	lokiCmd.AddCommand(lokiLabelsCmd)
	lokiLabelsCmd.Flags().StringVar(&lokiStart, "start", "", "Start time")
	lokiLabelsCmd.Flags().StringVar(&lokiEnd, "end", "", "End time")

	lokiCmd.AddCommand(lokiLabelValuesCmd)
	lokiLabelValuesCmd.Flags().StringVar(&lokiStart, "start", "", "Start time")
	lokiLabelValuesCmd.Flags().StringVar(&lokiEnd, "end", "", "End time")
}

var lokiQueryCmd = &cobra.Command{
	Use:   "query <datasource> <logql>",
	Short: "Execute a LogQL query",
	Args:  cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		ctx := context.Background()

		pc, cleanup, err := startProxy(ctx)
		if err != nil {
			return err
		}

		defer cleanup()

		params := url.Values{
			"query":     {args[1]},
			"limit":     {strconv.Itoa(lokiLimit)},
			"direction": {lokiDirection},
		}

		if lokiStart != "" {
			params.Set("start", lokiStart)
		}

		if lokiEnd != "" {
			params.Set("end", lokiEnd)
		}

		data, err := proxyGet(ctx, pc, "/loki/loki/api/v1/query_range", params, dsHeader(args[0]))
		if err != nil {
			return err
		}

		if lokiJSON {
			return printJSONBytes(data)
		}

		return printLokiResult(data)
	},
}

var lokiLabelsCmd = &cobra.Command{
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

		params := url.Values{}
		if lokiStart != "" {
			params.Set("start", lokiStart)
		}

		if lokiEnd != "" {
			params.Set("end", lokiEnd)
		}

		data, err := proxyGet(ctx, pc, "/loki/loki/api/v1/labels", params, dsHeader(args[0]))
		if err != nil {
			return err
		}

		if lokiJSON {
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

var lokiLabelValuesCmd = &cobra.Command{
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

		params := url.Values{}
		if lokiStart != "" {
			params.Set("start", lokiStart)
		}

		if lokiEnd != "" {
			params.Set("end", lokiEnd)
		}

		path := fmt.Sprintf("/loki/loki/api/v1/label/%s/values", args[1])

		data, err := proxyGet(ctx, pc, path, params, dsHeader(args[0]))
		if err != nil {
			return err
		}

		if lokiJSON {
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

// printLokiResult formats Loki query results as log lines.
func printLokiResult(data []byte) error {
	var resp struct {
		Data struct {
			Result []struct {
				Stream map[string]string `json:"stream"`
				Values [][]string        `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}

	if err := json.Unmarshal(data, &resp); err != nil {
		return printJSONBytes(data)
	}

	for _, stream := range resp.Data.Result {
		for _, entry := range stream.Values {
			if len(entry) < 2 {
				continue
			}

			fmt.Println(entry[1])
		}
	}

	return nil
}
