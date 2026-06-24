package cli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

var networksDevnetsOnly bool

var networksCmd = &cobra.Command{
	GroupID: groupDiscovery,
	Use:     "networks",
	Aliases: []string{"network"},
	Short:   "List active networks from Cartographoor",
	Long: `List active Ethereum networks from the authoritative Cartographoor-backed
network inventory.

Examples:
  panda networks
  panda networks --devnets
  panda devnets
  panda devnet list
  panda networks -o json
  panda networks info fusaka-devnet-3
  panda networks forks fusaka-devnet-3
  panda networks clients fusaka-devnet-3
  panda networks endpoints fusaka-devnet-3
  panda networks spec glamsterdam-devnet-5`,
	Args: cobra.NoArgs,
	RunE: runNetworks,
}

var networksListCmd = &cobra.Command{
	Use:   "list",
	Short: "List active networks",
	Args:  cobra.NoArgs,
	RunE:  runNetworks,
}

var devnetsCmd = &cobra.Command{
	GroupID: groupDiscovery,
	Use:     "devnets",
	Aliases: []string{"devnet"},
	Short:   "List active devnets from Cartographoor",
	Long: `List active devnet network ids from the authoritative Cartographoor-backed
network inventory.

Examples:
  panda devnets
  panda devnet list
  panda networks --devnets
  panda devnets -o json
  panda devnets info fusaka-devnet-3
  panda devnets endpoints fusaka-devnet-3`,
	Args: cobra.NoArgs,
	RunE: runDevnets,
}

var devnetsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List active devnets",
	Args:  cobra.NoArgs,
	RunE:  runDevnets,
}

type activeNetworksResponse struct {
	Networks           []activeNetworkSummary `json:"networks"`
	Groups             []string               `json:"groups"`
	ActiveDevnetGroups map[string][]string    `json:"active_devnet_groups"`
	Usage              string                 `json:"usage"`
}

type activeNetworkSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ChainID     uint64 `json:"chain_id,omitempty"`
	Status      string `json:"status"`
	IsDevnet    bool   `json:"is_devnet"`
	DevnetGroup string `json:"devnet_group,omitempty"`
	ResourceURI string `json:"resource_uri"`
}

func init() {
	rootCmd.AddCommand(networksCmd)
	rootCmd.AddCommand(devnetsCmd)

	networksCmd.Flags().BoolVar(&networksDevnetsOnly, "devnets", false, "Only show active devnet networks")
	networksCmd.AddCommand(networksListCmd)
	devnetsCmd.AddCommand(devnetsListCmd)

	// Detail subcommands work for any network or devnet id, under both groups.
	addNetworkDetailCommands(networksCmd)
	addNetworkDetailCommands(devnetsCmd)
	addNetworkSpecCommand(networksCmd)
	addNetworkSpecCommand(devnetsCmd)
}

// addNetworkSpecCommand attaches the `spec` view (notes.ethereum.org devnet
// spec page) to a parent command. With no section argument it prints the whole
// page; with one it prints just the matching section.
func addNetworkSpecCommand(parent *cobra.Command) {
	cmd := &cobra.Command{
		Use:   "spec <network> [section]",
		Short: "Show the notes.ethereum.org devnet spec page, or one of its sections",
		Long: `Show the notes.ethereum.org devnet spec page for a network.

With no section argument the full page markdown is printed. Pass a section
name (matched case-insensitively against headings, substring is enough) to
print just that section — e.g. the "Local testing" section carries the
Kurtosis config.

Examples:
  panda networks spec glamsterdam-devnet-5
  panda networks spec glamsterdam-devnet-5 --list
  panda networks spec glamsterdam-devnet-5 "local testing"
  panda networks spec glamsterdam-devnet-5 eip`,
		Args:              cobra.RangeArgs(1, 2),
		ValidArgsFunction: completeNetworkNames,
		RunE:              runNetworkSpec,
	}
	cmd.Flags().Bool("list", false, "List the section headings only")
	cmd.Flags().String("url", "", "Override the spec page URL (notes.ethereum.org or hackmd.io)")
	parent.AddCommand(cmd)
}

func runNetworkSpec(cmd *cobra.Command, args []string) error {
	listOnly, _ := cmd.Flags().GetBool("list")
	override, _ := cmd.Flags().GetString("url")

	opArgs := map[string]any{"network": args[0]}
	if override != "" {
		opArgs["url"] = override
	}

	response, err := runServerOperation(cmd, "network.spec", opArgs)
	if err != nil {
		return err
	}

	data, _ := response.Data.(map[string]any)
	sections := specSections(data["sections"])

	if listOnly {
		return printSpecHeadings(data, sections)
	}

	// A section argument prints just that section.
	if len(args) == 2 {
		return printSpecSection(data, sections, args[1])
	}

	if isJSON() {
		return printJSON(data)
	}

	fmt.Println(asString(data["markdown"]))

	return nil
}

type specSection struct {
	heading string
	content string
}

func specSections(value any) []specSection {
	raw, ok := value.([]any)
	if !ok {
		return nil
	}

	sections := make([]specSection, 0, len(raw))

	for _, item := range raw {
		section, ok := item.(map[string]any)
		if !ok {
			continue
		}

		sections = append(sections, specSection{
			heading: asString(section["heading"]),
			content: asString(section["content"]),
		})
	}

	return sections
}

func printSpecHeadings(data map[string]any, sections []specSection) error {
	if isJSON() {
		headings := make([]string, 0, len(sections))
		for _, section := range sections {
			headings = append(headings, section.heading)
		}

		return printJSON(map[string]any{"network": data["network"], "sections": headings})
	}

	if title := asString(data["title"]); title != "" {
		fmt.Println(title)
	}

	for _, section := range sections {
		fmt.Println("  - " + section.heading)
	}

	return nil
}

func printSpecSection(data map[string]any, sections []specSection, query string) error {
	match, ok := matchSpecSection(sections, query)
	if !ok {
		available := make([]string, 0, len(sections))
		for _, section := range sections {
			available = append(available, section.heading)
		}

		return fmt.Errorf("no section matching %q. Available: %s", query, strings.Join(available, ", "))
	}

	if isJSON() {
		return printJSON(map[string]any{
			"network": data["network"],
			"heading": match.heading,
			"content": match.content,
		})
	}

	fmt.Printf("## %s\n\n%s\n", match.heading, match.content)

	return nil
}

// matchSpecSection finds a section by query, in order of preference: exact
// heading, heading substring, then content substring (so "kurtosis" finds the
// "Local testing" section whose body mentions it).
func matchSpecSection(sections []specSection, query string) (specSection, bool) {
	lower := strings.ToLower(strings.TrimSpace(query))

	for _, section := range sections {
		if strings.EqualFold(section.heading, query) {
			return section, true
		}
	}

	for _, section := range sections {
		if strings.Contains(strings.ToLower(section.heading), lower) {
			return section, true
		}
	}

	for _, section := range sections {
		if strings.Contains(strings.ToLower(section.content), lower) {
			return section, true
		}
	}

	return specSection{}, false
}

// addNetworkDetailCommands attaches the per-network detail views to a parent
// command. Fresh instances are built per parent because a cobra command can
// only belong to one parent.
func addNetworkDetailCommands(parent *cobra.Command) {
	specs := []struct {
		use   string
		short string
		run   func(*cobra.Command, []string) error
	}{
		{"info <network>", "Show full details for a network or devnet", runNetworkInfo},
		{"forks <network>", "Show the fork schedule for a network or devnet", runNetworkForks},
		{"clients <network>", "Show client images and versions for a network or devnet", runNetworkClients},
		{"endpoints <network>", "Show service endpoints (rpc, beacon, dora, ...) for a network or devnet", runNetworkEndpoints},
	}

	for _, spec := range specs {
		cmd := &cobra.Command{
			Use:               spec.use,
			Short:             spec.short,
			Args:              cobra.ExactArgs(1),
			ValidArgsFunction: completeNetworkNames,
			RunE:              spec.run,
		}
		parent.AddCommand(cmd)
	}
}

func networkDetail(cmd *cobra.Command, id string) (map[string]any, error) {
	response, err := runServerOperation(cmd, "network.get", map[string]any{"network": id})
	if err != nil {
		return nil, err
	}

	data, _ := response.Data.(map[string]any)

	return data, nil
}

func runNetworkInfo(cmd *cobra.Command, args []string) error {
	data, err := networkDetail(cmd, args[0])
	if err != nil {
		return err
	}

	if isJSON() {
		return printJSON(data)
	}

	pairs := [][2]string{{"ID", asString(data["id"])}}
	for _, kv := range [][2]string{
		{"Name", "name"},
		{"Status", "status"},
		{"Chain ID", "chain_id"},
		{"Devnet group", "devnet_group"},
		{"Genesis time", "genesis_time"},
		{"Description", "description"},
		{"Node inventory", "node_inventory_url"},
		{"Hive", "hive_url"},
	} {
		if value := asString(data[kv[1]]); value != "" {
			pairs = append(pairs, [2]string{kv[0], value})
		}
	}

	printKeyValue(pairs)

	if rows := forkRows(data["forks"]); len(rows) > 0 {
		fmt.Println("\nForks:")
		printTable([]string{"LAYER", "FORK", "ACTIVATION"}, rows)
	}

	if rows := imageRows(data["clients"]); len(rows) > 0 {
		fmt.Println("\nClients:")
		printTable([]string{"CLIENT", "VERSION"}, rows)
	}

	if rows := endpointRows(data["endpoints"]); len(rows) > 0 {
		fmt.Println("\nEndpoints:")
		printTable([]string{"SERVICE", "URL"}, rows)
	}

	return nil
}

func runNetworkForks(cmd *cobra.Command, args []string) error {
	data, err := networkDetail(cmd, args[0])
	if err != nil {
		return err
	}

	if isJSON() {
		return printJSON(data["forks"])
	}

	rows := forkRows(data["forks"])
	if len(rows) == 0 {
		fmt.Printf("No forks advertised for %q.\n", args[0])

		return nil
	}

	printTable([]string{"LAYER", "FORK", "ACTIVATION"}, rows)

	return nil
}

func runNetworkClients(cmd *cobra.Command, args []string) error {
	data, err := networkDetail(cmd, args[0])
	if err != nil {
		return err
	}

	if isJSON() {
		return printJSON(data["clients"])
	}

	rows := imageRows(data["clients"])
	if len(rows) == 0 {
		fmt.Printf("No client images advertised for %q.\n", args[0])

		return nil
	}

	printTable([]string{"CLIENT", "VERSION"}, rows)

	return nil
}

func runNetworkEndpoints(cmd *cobra.Command, args []string) error {
	data, err := networkDetail(cmd, args[0])
	if err != nil {
		return err
	}

	if isJSON() {
		return printJSON(data["endpoints"])
	}

	rows := endpointRows(data["endpoints"])
	if len(rows) == 0 {
		fmt.Printf("No service endpoints advertised for %q.\n", args[0])

		return nil
	}

	printTable([]string{"SERVICE", "URL"}, rows)

	return nil
}

// forkRows flattens the forks object into LAYER/FORK/ACTIVATION rows, ordered
// by activation within each layer.
func forkRows(value any) [][]string {
	forks, ok := value.(map[string]any)
	if !ok {
		return nil
	}

	var rows [][]string

	for _, layer := range []struct {
		key   string
		label string
		point string
	}{
		{"consensus", "consensus", "epoch"},
		{"execution", "execution", "block"},
	} {
		entries, ok := forks[layer.key].(map[string]any)
		if !ok {
			continue
		}

		layerRows := make([][]string, 0, len(entries))

		for name, raw := range entries {
			fork, ok := raw.(map[string]any)
			if !ok {
				continue
			}

			activation := fmt.Sprintf("%s %s", layer.point, asString(fork[layer.point]))
			if ts := asString(fork["timestamp"]); ts != "" {
				activation += " (ts " + ts + ")"
			}

			layerRows = append(layerRows, []string{layer.label, name, activation})
		}

		sort.Slice(layerRows, func(i, j int) bool {
			return forkActivationValue(entries, layerRows[i][1], layer.point) <
				forkActivationValue(entries, layerRows[j][1], layer.point)
		})

		rows = append(rows, layerRows...)
	}

	return rows
}

func forkActivationValue(entries map[string]any, name, point string) float64 {
	fork, ok := entries[name].(map[string]any)
	if !ok {
		return 0
	}

	value, _ := fork[point].(float64)

	return value
}

func imageRows(value any) [][]string {
	images, ok := value.([]any)
	if !ok {
		return nil
	}

	rows := make([][]string, 0, len(images))

	for _, raw := range images {
		image, ok := raw.(map[string]any)
		if !ok {
			continue
		}

		rows = append(rows, []string{asString(image["name"]), asString(image["version"])})
	}

	return rows
}

func endpointRows(value any) [][]string {
	endpoints, ok := value.(map[string]any)
	if !ok {
		return nil
	}

	rows := make([][]string, 0, len(endpoints))

	for service, url := range endpoints {
		rows = append(rows, []string{service, asString(url)})
	}

	sort.Slice(rows, func(i, j int) bool {
		return rows[i][0] < rows[j][0]
	})

	return rows
}

// asString renders a JSON-decoded scalar without trailing ".0" on whole numbers.
func asString(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case float64:
		if v == float64(int64(v)) {
			return fmt.Sprintf("%d", int64(v))
		}

		return fmt.Sprintf("%v", v)
	default:
		return fmt.Sprintf("%v", v)
	}
}

func runNetworks(cmd *cobra.Command, _ []string) error {
	return runNetworksFiltered(cmd, networksDevnetsOnly)
}

func runDevnets(cmd *cobra.Command, _ []string) error {
	return runNetworksFiltered(cmd, true)
}

func runNetworksFiltered(cmd *cobra.Command, devnetsOnly bool) error {
	response, err := readResource(cmd.Context(), "networks://active")
	if err != nil {
		return fmt.Errorf("reading active networks: %w", err)
	}

	var payload activeNetworksResponse
	if err := json.Unmarshal([]byte(response.Content), &payload); err != nil {
		return fmt.Errorf("decoding active networks: %w", err)
	}

	if devnetsOnly {
		payload.Networks = filterDevnetNetworks(payload.Networks)
		payload.Groups = sortedActiveDevnetGroups(payload.ActiveDevnetGroups)
	}

	if isJSON() {
		return printJSON(payload)
	}

	return printNetworks(payload, devnetsOnly)
}

func printNetworks(response activeNetworksResponse, devnetsOnly bool) error {
	if len(response.Networks) == 0 {
		if devnetsOnly {
			fmt.Println("No active devnets found.")
		} else {
			fmt.Println("No active networks found.")
		}

		return nil
	}

	rows := make([][]string, 0, len(response.Networks))
	for _, network := range response.Networks {
		group := network.DevnetGroup
		if group == "" {
			group = "-"
		}

		chainID := ""
		if network.ChainID != 0 {
			chainID = fmt.Sprint(network.ChainID)
		}

		rows = append(rows, []string{
			network.ID,
			network.Name,
			network.Status,
			chainID,
			group,
		})
	}

	sort.Slice(rows, func(i, j int) bool {
		return rows[i][0] < rows[j][0]
	})

	printTable([]string{"ID", "NAME", "STATUS", "CHAIN_ID", "DEVNET_GROUP"}, rows)
	fmt.Println("\nSource: networks://active (Cartographoor)")

	return nil
}

func filterDevnetNetworks(networks []activeNetworkSummary) []activeNetworkSummary {
	filtered := make([]activeNetworkSummary, 0, len(networks))

	for _, network := range networks {
		if network.IsDevnet {
			filtered = append(filtered, network)
		}
	}

	return filtered
}

func sortedActiveDevnetGroups(groups map[string][]string) []string {
	names := make([]string, 0, len(groups))

	for name, networks := range groups {
		if len(networks) > 0 {
			names = append(names, name)
		}
	}

	sort.Strings(names)

	return names
}
