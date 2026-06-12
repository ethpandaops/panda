package cli

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

var cbtCmd = &cobra.Command{
	GroupID: groupDirect,
	Use:     "cbt",
	Short:   "Query CBT pipeline status: models, table bounds, coverage",
	Long: `Query CBT, the transformation pipeline that builds the pre-aggregated
analytics tables (fct_*, int_*, dim_*). Model IDs are database.table.

Incremental transformations track processed (position, interval) ranges; the
position unit depends on the model's interval type (unix seconds for
slot-type models, block number for block-type models — see 'panda cbt
intervals').

Examples:
  panda cbt networks
  panda cbt models mainnet --search fct_block
  panda cbt bounds mainnet
  panda cbt bounds mainnet default.canonical_beacon_block
  panda cbt transformations mainnet --type incremental
  panda cbt transformation mainnet mainnet.fct_block
  panda cbt coverage mainnet mainnet.fct_block
  panda cbt coverage mainnet mainnet.fct_block --at 1749600000
  panda cbt runs mainnet
  panda cbt link mainnet mainnet.fct_block`,
}

var (
	cbtModelsType       string
	cbtModelsDatabase   string
	cbtModelsSearch     string
	cbtExternalDatabase string
	cbtTransformsType   string
	cbtTransformsDB     string
	cbtTransformsState  string
	cbtCoverageAt       int64
)

func init() {
	rootCmd.AddCommand(cbtCmd)

	cbtCmd.AddCommand(
		cbtNetworksCmd,
		cbtModelsCmd,
		cbtExternalCmd,
		cbtBoundsCmd,
		cbtTransformationsCmd,
		cbtTransformationCmd,
		cbtCoverageCmd,
		cbtRunsCmd,
		cbtIntervalsCmd,
		cbtLinkCmd,
	)

	for _, cmd := range []*cobra.Command{
		cbtModelsCmd,
		cbtExternalCmd,
		cbtBoundsCmd,
		cbtTransformationsCmd,
		cbtTransformationCmd,
		cbtCoverageCmd,
		cbtRunsCmd,
		cbtIntervalsCmd,
		cbtLinkCmd,
	} {
		cmd.ValidArgsFunction = completeCBTNetworkNames
	}

	cbtModelsCmd.Flags().StringVar(&cbtModelsType, "type", "", "Filter by model type (external, transformation)")
	cbtModelsCmd.Flags().StringVar(&cbtModelsDatabase, "database", "", "Filter by database")
	cbtModelsCmd.Flags().StringVar(&cbtModelsSearch, "search", "", "Search models by name")

	cbtExternalCmd.Flags().StringVar(&cbtExternalDatabase, "database", "", "Filter the listing by database")

	cbtTransformationsCmd.Flags().StringVar(&cbtTransformsType, "type", "", "Filter by type (scheduled, incremental)")
	cbtTransformationsCmd.Flags().StringVar(&cbtTransformsDB, "database", "", "Filter by database")
	cbtTransformationsCmd.Flags().StringVar(&cbtTransformsState, "status", "", "Filter by status (success, failed, running, pending)")

	cbtCoverageCmd.Flags().Int64Var(&cbtCoverageAt, "at", -1,
		"Debug a specific position: explain coverage, dependency bounds, and blocking gaps (requires a model ID)")
}

var cbtNetworksCmd = &cobra.Command{
	Use:   "networks",
	Short: "List networks with CBT instances",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		response, err := runServerOperation(cmd, "cbt.list_networks", map[string]any{})
		if err != nil {
			return err
		}

		return printListing(response, "networks", "No networks with CBT instances found.")
	},
}

var cbtModelsCmd = &cobra.Command{
	Use:   "models <network>",
	Short: "List data models (external sources and transformations)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		opArgs := map[string]any{"network": args[0]}
		setIfNotEmpty(opArgs, "type", cbtModelsType)
		setIfNotEmpty(opArgs, "database", cbtModelsDatabase)
		setIfNotEmpty(opArgs, "search", cbtModelsSearch)

		body, err := runCBTPassthrough(cmd, "cbt.list_models", opArgs)
		if err != nil || body == nil {
			return err
		}

		models, _ := body["models"].([]any)
		if len(models) == 0 {
			fmt.Println("No models found.")
			return nil
		}

		fmt.Printf("Models (%v total):\n", formatJSONNumber(body["total"]))
		for _, item := range models {
			model, _ := item.(map[string]any)
			fmt.Printf("  %v  (%v)\n", model["id"], model["type"])
		}

		return nil
	},
}

var cbtExternalCmd = &cobra.Command{
	Use:   "external <network> [id]",
	Short: "List external source models, or get one by ID (detail is always JSON)",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 2 {
			response, err := runServerOperationRaw(cmd, "cbt.get_external_model", map[string]any{
				"network": args[0],
				"id":      args[1],
			})
			if err != nil {
				return err
			}

			return printJSONBytes(response.Body)
		}

		opArgs := map[string]any{"network": args[0]}
		setIfNotEmpty(opArgs, "database", cbtExternalDatabase)

		body, err := runCBTPassthrough(cmd, "cbt.list_external_models", opArgs)
		if err != nil || body == nil {
			return err
		}

		models, _ := body["models"].([]any)
		if len(models) == 0 {
			fmt.Println("No external models found.")
			return nil
		}

		fmt.Printf("External models (%v total):\n", formatJSONNumber(body["total"]))
		for _, item := range models {
			model, _ := item.(map[string]any)
			fmt.Printf("  %v\n", model["id"])
		}

		return nil
	},
}

var cbtBoundsCmd = &cobra.Command{
	Use:   "bounds <network> [id]",
	Short: "Show min/max available positions for external source models",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		opArgs := map[string]any{"network": args[0]}
		if len(args) == 2 {
			opArgs["id"] = args[1]
		}

		body, err := runCBTPassthrough(cmd, "cbt.get_external_bounds", opArgs)
		if err != nil {
			if len(args) == 2 {
				return fmt.Errorf(
					"%w\n\n  hint: 'bounds' covers external source models; for transformations (fct_*/int_*/dim_*) use 'panda cbt coverage %s %s'",
					err, args[0], args[1])
			}

			return err
		}

		if body == nil {
			return nil
		}

		bounds, ok := body["bounds"].([]any)
		if !ok {
			// Single-model response.
			bounds = []any{body}
		}

		if len(bounds) == 0 {
			fmt.Println("No external bounds found.")
			return nil
		}

		for _, item := range bounds {
			entry, _ := item.(map[string]any)
			fmt.Printf(
				"  %v  %v..%v%s  last_scan=%v\n",
				entry["id"],
				formatJSONNumber(entry["min"]),
				formatJSONNumber(entry["max"]),
				formatCBTRangeTime(entry["min"], entry["max"]),
				entry["last_incremental_scan"],
			)
		}

		return nil
	},
}

var cbtTransformationsCmd = &cobra.Command{
	Use:   "transformations <network>",
	Short: "List transformations",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		opArgs := map[string]any{"network": args[0]}
		setIfNotEmpty(opArgs, "type", cbtTransformsType)
		setIfNotEmpty(opArgs, "database", cbtTransformsDB)
		setIfNotEmpty(opArgs, "status", cbtTransformsState)

		body, err := runCBTPassthrough(cmd, "cbt.list_transformations", opArgs)
		if err != nil || body == nil {
			return err
		}

		models, _ := body["models"].([]any)
		if len(models) == 0 {
			fmt.Println("No transformations found.")
			return nil
		}

		fmt.Printf("Transformations (%v total):\n", formatJSONNumber(body["total"]))
		for _, item := range models {
			model, _ := item.(map[string]any)
			fmt.Printf("  %v  (%v)\n", model["id"], model["type"])
		}

		return nil
	},
}

var cbtTransformationCmd = &cobra.Command{
	Use:   "transformation <network> <id>",
	Short: "Get a transformation's definition: SQL, schedules, dependencies (always JSON)",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		response, err := runServerOperationRaw(cmd, "cbt.get_transformation", map[string]any{
			"network": args[0],
			"id":      args[1],
		})
		if err != nil {
			return err
		}

		return printJSONBytes(response.Body)
	},
}

var cbtCoverageCmd = &cobra.Command{
	Use:   "coverage <network> [id]",
	Short: "Show processed (position, interval) ranges for transformations",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if cbtCoverageAt >= 0 {
			if len(args) != 2 {
				return fmt.Errorf("--at requires a model ID")
			}

			response, err := runServerOperationRaw(cmd, "cbt.debug_coverage", map[string]any{
				"network":  args[0],
				"id":       args[1],
				"position": cbtCoverageAt,
			})
			if err != nil {
				return err
			}

			return printJSONBytes(response.Body)
		}

		opArgs := map[string]any{"network": args[0]}
		if len(args) == 2 {
			opArgs["id"] = args[1]
		}

		body, err := runCBTPassthrough(cmd, "cbt.get_transformation_coverage", opArgs)
		if err != nil {
			if len(args) == 2 {
				return fmt.Errorf(
					"%w\n\n  hint: 'coverage' covers transformations; for external source tables use 'panda cbt bounds %s %s'",
					err, args[0], args[1])
			}

			return err
		}

		if body == nil {
			return nil
		}

		coverage, ok := body["coverage"].([]any)
		if !ok {
			// Single-model response.
			coverage = []any{body}
		}

		if len(coverage) == 0 {
			fmt.Println("No coverage found.")
			return nil
		}

		for _, item := range coverage {
			entry, _ := item.(map[string]any)
			fmt.Printf("  %v\n", entry["id"])

			ranges, _ := entry["ranges"].([]any)
			if len(ranges) > 1 {
				fmt.Printf("    (%d ranges — gaps between them are unprocessed)\n", len(ranges))
			}

			for _, r := range ranges {
				coverageRange, _ := r.(map[string]any)
				position, _ := coverageRange["position"].(float64)
				interval, _ := coverageRange["interval"].(float64)
				fmt.Printf(
					"    position=%v interval=%v%s\n",
					formatJSONNumber(coverageRange["position"]),
					formatJSONNumber(coverageRange["interval"]),
					formatCBTRangeTime(position, position+interval),
				)
			}
		}

		return nil
	},
}

var cbtRunsCmd = &cobra.Command{
	Use:   "runs <network> [id]",
	Short: "Show last-run times for scheduled transformations",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		opArgs := map[string]any{"network": args[0]}
		if len(args) == 2 {
			opArgs["id"] = args[1]
		}

		body, err := runCBTPassthrough(cmd, "cbt.get_scheduled_runs", opArgs)
		if err != nil || body == nil {
			return err
		}

		runs, ok := body["runs"].([]any)
		if !ok {
			// Single-model response.
			runs = []any{body}
		}

		if len(runs) == 0 {
			fmt.Println("No scheduled runs found.")
			return nil
		}

		for _, item := range runs {
			run, _ := item.(map[string]any)
			fmt.Printf("  %v  last_run=%v\n", run["id"], run["last_run"])
		}

		return nil
	},
}

var cbtIntervalsCmd = &cobra.Command{
	Use:   "intervals <network>",
	Short: "Show interval type definitions mapping positions to human units (always JSON)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		response, err := runServerOperationRaw(cmd, "cbt.get_interval_types", map[string]any{
			"network": args[0],
		})
		if err != nil {
			return err
		}

		return printJSONBytes(response.Body)
	},
}

var cbtLinkCmd = &cobra.Command{
	Use:   "link <network> <id>",
	Short: "Print a deep link to a model in the CBT web UI",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		response, err := runServerOperation(cmd, "cbt.link_model", map[string]any{
			"network": args[0],
			"id":      args[1],
		})
		if err != nil {
			return err
		}

		if isJSON() {
			return printJSON(response)
		}

		data, _ := response.Data.(map[string]any)
		fmt.Printf("%v\n", data["url"])

		return nil
	},
}

// runCBTPassthrough runs a CBT passthrough operation and decodes the JSON
// body for human-readable rendering. In JSON output mode it prints the raw
// body and returns (nil, nil) so callers skip rendering.
func runCBTPassthrough(cmd *cobra.Command, operationID string, args map[string]any) (map[string]any, error) {
	response, err := runServerOperationRaw(cmd, operationID, args)
	if err != nil {
		return nil, err
	}

	if isJSON() {
		return nil, printJSONBytes(response.Body)
	}

	var body map[string]any
	if err := json.Unmarshal(response.Body, &body); err != nil {
		return nil, fmt.Errorf("decoding CBT response: %w", err)
	}

	return body, nil
}

// setIfNotEmpty sets args[key] = value when the value is non-empty.
func setIfNotEmpty(args map[string]any, key, value string) {
	if value != "" {
		args[key] = value
	}
}

// formatCBTRangeTime renders a position range as UTC datetimes when both ends
// look like unix-second positions (slot- and second-type models use unix
// seconds). Block-number and entity positions fall outside the window and get
// no annotation.
func formatCBTRangeTime(minValue, maxValue any) string {
	start, okStart := cbtPositionTime(minValue)
	end, okEnd := cbtPositionTime(maxValue)

	if !okStart || !okEnd {
		return ""
	}

	return fmt.Sprintf("  (%s → %s)", start, end)
}

// cbtPositionTime interprets a position as a unix-second timestamp, accepting
// only values between 2010 and 2096 so non-time positions are left alone.
func cbtPositionTime(value any) (string, bool) {
	number, ok := value.(float64)
	if !ok || number < 1.26e9 || number > 4e9 {
		return "", false
	}

	return time.Unix(int64(number), 0).UTC().Format(time.RFC3339), true
}
