package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/spf13/cobra"
)

var datasetsCmd = &cobra.Command{
	GroupID: groupDiscovery,
	Use:     "datasets [name]",
	Short:   "List datasets in this deployment, or show one dataset's query guide",
	Long: `Without arguments, list the datasets this deployment holds, one row per
placement: which datasource holds the dataset, in which database (params),
with any operator notes.

With a dataset name, show its full query guide: placement in this deployment,
required syntax rules, and example categories. Read it before querying the
dataset.

Examples:
  panda datasets            # List datasets and placements
  panda datasets <name>     # Show that dataset's query guide
  panda datasets --json     # Output the list as JSON`,
	Args:              cobra.MaximumNArgs(1),
	ValidArgsFunction: completeDatasetNames,
	RunE:              runDatasets,
}

// datasetListEntry mirrors one entry of the datasets://list resource payload.
type datasetListEntry struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Active      bool   `json:"active"`
	Placements  []struct {
		Datasource string            `json:"datasource"`
		Params     map[string]string `json:"params"`
		Notes      string            `json:"notes"`
	} `json:"placements"`
}

func init() {
	rootCmd.AddCommand(datasetsCmd)
}

func runDatasets(cmd *cobra.Command, args []string) error {
	if len(args) == 1 {
		guide, err := readResource(cmd.Context(), "datasets://"+args[0])
		if err != nil {
			return fmt.Errorf("reading dataset guide: %w", err)
		}

		fmt.Println(guide.Content)

		return nil
	}

	response, err := readResource(cmd.Context(), "datasets://list")
	if err != nil {
		return fmt.Errorf("reading datasets list: %w", err)
	}

	var parsed struct {
		Datasets []datasetListEntry `json:"datasets"`
	}

	if err := json.Unmarshal([]byte(response.Content), &parsed); err != nil {
		return fmt.Errorf("parsing datasets list: %w", err)
	}

	if isJSON() {
		return printJSON(parsed)
	}

	type noteLine struct{ key, note string }

	rows := make([][]string, 0, len(parsed.Datasets))

	var notes []noteLine
	var blankPlacementRows bool

	for _, d := range parsed.Datasets {
		// Inactive datasets are known to the release but absent from this
		// deployment — listing them would suggest data that isn't there.
		if !d.Active {
			continue
		}

		if len(d.Placements) == 0 {
			blankPlacementRows = true
			rows = append(rows, []string{d.Name, "", "", d.Description})

			continue
		}

		for _, p := range d.Placements {
			rows = append(rows, []string{d.Name, p.Datasource, formatBindingParams(p.Params), d.Description})

			if p.Notes != "" {
				key := d.Name + " @ " + p.Datasource
				if len(p.Params) > 0 {
					key += " (" + formatBindingParams(p.Params) + ")"
				}

				notes = append(notes, noteLine{key: key, note: p.Notes})
			}
		}
	}

	if len(rows) == 0 {
		fmt.Println("No datasets found.")

		return nil
	}

	sort.Slice(rows, func(i, j int) bool {
		if rows[i][0] != rows[j][0] {
			return rows[i][0] < rows[j][0]
		}

		return rows[i][1]+rows[i][2] < rows[j][1]+rows[j][2]
	})

	printTable([]string{"DATASET", "DATASOURCE", "PARAMS", "DESCRIPTION"}, rows)

	if len(notes) > 0 {
		fmt.Println("\nNotes:")

		for _, n := range notes {
			fmt.Printf("  %s: %s\n", n.key, n.note)
		}
	}

	if blankPlacementRows {
		fmt.Println("\nBlank datasource means this server did not advertise dataset placement metadata. Use the `Target` shown by `panda search examples \"<topic>\"`, or inspect concrete datasources with `panda resources datasources://clickhouse`.")
	}

	fmt.Println("\nRead a dataset's guide: panda datasets <name>")

	return nil
}

// completeDatasetNames offers active dataset names for shell completion.
func completeDatasetNames(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	response, err := readResource(cmd.Context(), "datasets://list")
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	var parsed struct {
		Datasets []datasetListEntry `json:"datasets"`
	}

	if err := json.Unmarshal([]byte(response.Content), &parsed); err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	names := make([]string, 0, len(parsed.Datasets))

	for _, d := range parsed.Datasets {
		if d.Active {
			names = append(names, d.Name)
		}
	}

	return names, cobra.ShellCompDirectiveNoFileComp
}

func isActiveDatasetName(ctx context.Context, name string) bool {
	response, err := readResource(ctx, "datasets://list")
	if err != nil {
		return false
	}

	var parsed struct {
		Datasets []datasetListEntry `json:"datasets"`
	}

	if err := json.Unmarshal([]byte(response.Content), &parsed); err != nil {
		return false
	}

	for _, d := range parsed.Datasets {
		if d.Active && d.Name == name {
			return true
		}
	}

	return false
}
