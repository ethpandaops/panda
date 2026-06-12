package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var blockArchiveCmd = &cobra.Command{
	GroupID: groupDirect,
	Use:     "block-archive",
	Short:   "Fetch raw beacon blocks from the block archive",
	Long: `Fetch canonical beacon blocks (SSZ or decoded JSON) from the public block archive.

Block-root lookups are intended to be sourced from clickhouse (clickhouse-raw) — this
command group is the raw-data fetcher, not a search/index API.

Examples:
  panda block-archive networks
  panda block-archive download mainnet 9000000 0x... --out block.ssz
  panda block-archive get mainnet 9000000 0x...
  panda block-archive link mainnet 9000000 0x...`,
}

var blockArchiveNetworksAll bool

var blockArchiveNetworksCmd = &cobra.Command{
	Use:   "networks",
	Short: "List networks served by the archive (active by default; use --all to include inactive)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		args := map[string]any{}
		if !blockArchiveNetworksAll {
			args["active"] = true
		}

		response, err := runServerOperation(cmd, "block_archive.list_networks", args)
		if err != nil {
			return err
		}

		return printListing(response, "networks", "No networks reported by the block archive.")
	},
}

var blockArchiveDownloadOut string

var blockArchiveDownloadCmd = &cobra.Command{
	Use:   "download <network> <slot> <block-root>",
	Short: "Download the SSZ bytes for a block",
	Long: `Download the SSZ bytes for a block.

Use --out to write the bytes to a local file; otherwise bytes are written to stdout.`,
	Args: cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		response, err := runServerOperationRaw(cmd, "block_archive.download_ssz", map[string]any{
			"network":    args[0],
			"slot":       args[1],
			"block_root": args[2],
		})
		if err != nil {
			return err
		}

		if blockArchiveDownloadOut == "" || blockArchiveDownloadOut == "-" {
			_, err := os.Stdout.Write(response.Body)
			return err
		}

		if err := os.WriteFile(blockArchiveDownloadOut, response.Body, 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", blockArchiveDownloadOut, err)
		}

		fmt.Fprintf(os.Stderr, "wrote %d bytes to %s\n", len(response.Body), blockArchiveDownloadOut)

		return nil
	},
}

var blockArchiveGetCmd = &cobra.Command{
	Use:   "get <network> <slot> <block-root>",
	Short: "Get the decoded JSON SignedBeaconBlock (always JSON)",
	Args:  cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		response, err := runServerOperationRaw(cmd, "block_archive.get_block_json", map[string]any{
			"network":    args[0],
			"slot":       args[1],
			"block_root": args[2],
		})
		if err != nil {
			return err
		}

		return printJSONBytes(response.Body)
	},
}

var blockArchiveLinkCmd = &cobra.Command{
	Use:   "link <network> <slot> <block-root>",
	Short: "Print a browser link for a (network, slot, block_root)",
	Args:  cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		response, err := runServerOperation(cmd, "block_archive.link", map[string]any{
			"network":    args[0],
			"slot":       args[1],
			"block_root": args[2],
		})
		if err != nil {
			return err
		}

		if isJSON() {
			return printJSON(response)
		}

		data, _ := response.Data.(map[string]any)
		url, _ := data["url"].(string)
		fmt.Println(url)

		return nil
	},
}

func init() {
	rootCmd.AddCommand(blockArchiveCmd)

	blockArchiveDownloadCmd.Flags().StringVar(&blockArchiveDownloadOut, "out", "",
		"Output file (default: stdout)")
	blockArchiveNetworksCmd.Flags().BoolVar(&blockArchiveNetworksAll, "all", false,
		"Include inactive networks the archive has historical blocks for")

	blockArchiveCmd.AddCommand(
		blockArchiveNetworksCmd,
		blockArchiveDownloadCmd,
		blockArchiveGetCmd,
		blockArchiveLinkCmd,
	)
}
