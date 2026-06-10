package cli

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/spf13/cobra"
)

var datasetsCmd = &cobra.Command{
	GroupID: groupDiscovery,
	Use:     "datasets",
	Short:   "List datasets in this deployment and where they live",
	Long: `List the datasets this deployment holds, one row per placement: which
datasource holds the dataset, in which database (params), with any operator
notes. Read a dataset's full query guide with:

  panda resources read datasets://<name>

Examples:
  panda datasets          # List datasets and placements
  panda datasets --json   # Output as JSON`,
	RunE: runDatasets,
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

func runDatasets(cmd *cobra.Command, _ []string) error {
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

	for _, d := range parsed.Datasets {
		// Inactive datasets are known to the release but absent from this
		// deployment — listing them would suggest data that isn't there.
		if !d.Active {
			continue
		}

		if len(d.Placements) == 0 {
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

	fmt.Println("\nRead a dataset's guide: panda resources read datasets://<name>")

	return nil
}
