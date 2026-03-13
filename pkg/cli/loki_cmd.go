package cli

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/ethpandaops/panda/pkg/operations"
	"github.com/spf13/cobra"
)

var (
	lokiLimit     int
	lokiStart     string
	lokiEnd       string
	lokiDirection string
	lokiTime      string
)

var lokiCmd = &cobra.Command{
	GroupID: groupDirect,
	Use:     "loki",
	Short:   "Query Loki logs",
	Long: `Query Loki for log data.

Examples:
  panda loki list-datasources
  panda loki query ethpandaops '{app="beacon-node"}'
  panda loki query ethpandaops '{app="beacon-node"} |= "error"' --limit 50 --start now-1h
  panda loki labels ethpandaops
  panda loki label-values ethpandaops app`,
}

func init() {
	rootCmd.AddCommand(lokiCmd)

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
		items, err := listLokiDatasources()
		if err != nil {
			return err
		}

		if lokiJSON || isJSON() {
			return printJSON(operations.DatasourcesPayload{Datasources: items})
		}

		if len(items) == 0 {
			fmt.Println("No Loki datasources found.")
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

var lokiQueryCmd = &cobra.Command{
	Use:   "query <datasource> <logql>",
	Short: "Execute a LogQL range query",
	Args:  cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		response, err := lokiQuery(operations.LokiQueryArgs{
			Datasource: args[0],
			Query:      args[1],
			Limit:      lokiLimit,
			Start:      lokiStart,
			End:        lokiEnd,
			Direction:  lokiDirection,
		})
		if err != nil {
			return err
		}

		if lokiJSON || isJSON() {
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
		response, err := lokiInstantQuery(operations.LokiInstantQueryArgs{
			Datasource: args[0],
			Query:      args[1],
			Limit:      lokiLimit,
			Time:       lokiTime,
			Direction:  lokiDirection,
		})
		if err != nil {
			return err
		}

		if lokiJSON || isJSON() {
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
		response, err := lokiLabels(operations.LokiLabelsArgs{
			Datasource: args[0],
			Start:      lokiStart,
			End:        lokiEnd,
		})
		if err != nil {
			return err
		}

		if lokiJSON || isJSON() {
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
		response, err := lokiLabelValues(operations.LokiLabelValuesArgs{
			Datasource: args[0],
			Label:      args[1],
			Start:      lokiStart,
			End:        lokiEnd,
		})
		if err != nil {
			return err
		}

		if lokiJSON || isJSON() {
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
		labelStr := formatLabelSet(stream.Stream, false)

		for _, entry := range stream.Values {
			if len(entry) < 2 {
				continue
			}

			ts := entry[0]

			nsec, err := strconv.ParseInt(ts, 10, 64)
			if err == nil {
				ts = time.Unix(0, nsec).UTC().Format("2006-01-02T15:04:05.000Z")
			}

			fmt.Printf("%s %s %s\n", ts, labelStr, entry[1])
		}
	}

	return nil
}
