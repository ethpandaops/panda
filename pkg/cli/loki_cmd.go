package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

var (
	lokiJSON      bool
	lokiLimit     int
	lokiStart     string
	lokiEnd       string
	lokiDirection string
	lokiTime      string
)

var lokiCmd = &cobra.Command{
	Use:   "loki",
	Short: "Query Loki logs",
	Long: `Query Loki for log data.

Examples:
  ep loki list-datasources
  ep loki query ethpandaops '{app="beacon-node"}'
  ep loki query ethpandaops '{app="beacon-node"} |= "error"' --limit 50 --start now-1h
  ep loki labels ethpandaops
  ep loki label-values ethpandaops app`,
}

func init() {
	rootCmd.AddCommand(lokiCmd)
	lokiCmd.PersistentFlags().BoolVar(&lokiJSON, "json", false, "Output in JSON format")

	lokiCmd.AddCommand(lokiListDatasourcesCmd)
	lokiCmd.AddCommand(lokiQueryCmd)
	lokiQueryCmd.Flags().IntVar(&lokiLimit, "limit", 100, "Maximum entries to return")
	lokiQueryCmd.Flags().StringVar(&lokiStart, "start", "", "Start time (RFC3339, unix, or 'now-1h')")
	lokiQueryCmd.Flags().StringVar(&lokiEnd, "end", "", "End time (RFC3339, unix, or 'now')")
	lokiQueryCmd.Flags().StringVar(&lokiDirection, "direction", "backward", "Sort direction: forward or backward")

	lokiCmd.AddCommand(lokiQueryInstantCmd)
	lokiQueryInstantCmd.Flags().IntVar(&lokiLimit, "limit", 100, "Maximum entries to return")
	lokiQueryInstantCmd.Flags().StringVar(&lokiTime, "time", "", "Evaluation timestamp (RFC3339, unix, or 'now')")
	lokiQueryInstantCmd.Flags().StringVar(&lokiDirection, "direction", "backward", "Sort direction: forward or backward")

	lokiCmd.AddCommand(lokiLabelsCmd)
	lokiLabelsCmd.Flags().StringVar(&lokiStart, "start", "", "Start time")
	lokiLabelsCmd.Flags().StringVar(&lokiEnd, "end", "", "End time")

	lokiCmd.AddCommand(lokiLabelValuesCmd)
	lokiLabelValuesCmd.Flags().StringVar(&lokiStart, "start", "", "Start time")
	lokiLabelValuesCmd.Flags().StringVar(&lokiEnd, "end", "", "End time")

	lokiQueryCmd.ValidArgsFunction = completeDatasourceNames("loki")
	lokiQueryInstantCmd.ValidArgsFunction = completeDatasourceNames("loki")
	lokiLabelsCmd.ValidArgsFunction = completeDatasourceNames("loki")
	lokiLabelValuesCmd.ValidArgsFunction = completeDatasourceNames("loki")
	_ = lokiQueryCmd.RegisterFlagCompletionFunc("direction", cobra.FixedCompletions(
		[]string{"forward", "backward"}, cobra.ShellCompDirectiveNoFileComp,
	))
	_ = lokiQueryInstantCmd.RegisterFlagCompletionFunc("direction", cobra.FixedCompletions(
		[]string{"forward", "backward"}, cobra.ShellCompDirectiveNoFileComp,
	))
}

var lokiListDatasourcesCmd = &cobra.Command{
	Use:   "list-datasources",
	Short: "List available Loki datasources",
	RunE: func(_ *cobra.Command, _ []string) error {
		response, err := runServerOperation("loki.list_datasources", map[string]any{})
		if err != nil {
			return err
		}

		if lokiJSON {
			return printJSON(response)
		}

		data, _ := response.Data.(map[string]any)
		items, _ := data["datasources"].([]any)
		if len(items) == 0 {
			fmt.Println("No Loki datasources found.")
			return nil
		}

		for _, item := range items {
			ds, _ := item.(map[string]any)
			name, _ := ds["name"].(string)
			desc, _ := ds["description"].(string)
			targetURL, _ := ds["url"].(string)

			if targetURL != "" {
				fmt.Printf("  %-16s  %-24s  %s\n", name, desc, targetURL)
				continue
			}

			fmt.Printf("  %-16s  %s\n", name, desc)
		}

		return nil
	},
}

var lokiQueryCmd = &cobra.Command{
	Use:   "query <datasource> <logql>",
	Short: "Execute a LogQL range query",
	Args:  cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		response, err := runServerOperationRaw("loki.query", map[string]any{
			"datasource": args[0],
			"query":      args[1],
			"limit":      lokiLimit,
			"start":      lokiStart,
			"end":        lokiEnd,
			"direction":  lokiDirection,
		})
		if err != nil {
			return err
		}

		if lokiJSON {
			return printJSONBytes(response.Body)
		}

		return printLokiResult(response.Body)
	},
}

var lokiQueryInstantCmd = &cobra.Command{
	Use:   "query-instant <datasource> <logql>",
	Short: "Execute an instant LogQL query",
	Args:  cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		response, err := runServerOperationRaw("loki.query_instant", map[string]any{
			"datasource": args[0],
			"query":      args[1],
			"limit":      lokiLimit,
			"time":       lokiTime,
			"direction":  lokiDirection,
		})
		if err != nil {
			return err
		}

		if lokiJSON {
			return printJSONBytes(response.Body)
		}

		return printLokiResult(response.Body)
	},
}

var lokiLabelsCmd = &cobra.Command{
	Use:   "labels <datasource>",
	Short: "List all label names",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		response, err := runServerOperationRaw("loki.get_labels", map[string]any{
			"datasource": args[0],
			"start":      lokiStart,
			"end":        lokiEnd,
		})
		if err != nil {
			return err
		}

		if lokiJSON {
			return printJSONBytes(response.Body)
		}

		return printAPIStringValues(response.Body)
	},
}

var lokiLabelValuesCmd = &cobra.Command{
	Use:   "label-values <datasource> <label>",
	Short: "Get all values for a label",
	Args:  cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		response, err := runServerOperationRaw("loki.get_label_values", map[string]any{
			"datasource": args[0],
			"label":      args[1],
			"start":      lokiStart,
			"end":        lokiEnd,
		})
		if err != nil {
			return err
		}

		if lokiJSON {
			return printJSONBytes(response.Body)
		}

		return printAPIStringValues(response.Body)
	},
}

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
