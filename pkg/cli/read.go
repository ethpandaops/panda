package cli

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
)

var readCmd = &cobra.Command{
	GroupID: groupWorkflow,
	Use:     "read <ref>",
	Short:   "Read the full content of a search result by its ref URI",
	Long: `Read the full content of a search result by its ref URI.

Refs are returned by 'panda search' as "Read full content: panda read <ref>".

Supported ref schemes:
  runbooks://{name}             Full runbook markdown
  eips://{number}               Full EIP content
  consensus-specs://{fork}/{topic}  Full consensus spec document

Examples:
  panda read runbooks://finality_delay
  panda read eips://4844
  panda read consensus-specs://deneb/beacon-chain`,
	Args: cobra.ExactArgs(1),
	RunE: runRead,
}

func init() {
	rootCmd.AddCommand(readCmd)
}

func runRead(cmd *cobra.Command, args []string) error {
	ref := strings.TrimSpace(args[0])
	if ref == "" {
		return fmt.Errorf("ref URI is required")
	}

	query := url.Values{"uri": []string{ref}}

	data, status, _, err := serverDo(cmd.Context(), "GET", "/api/v1/resources/read", nil, query, nil)
	if err != nil {
		return fmt.Errorf("reading %s: %w", ref, err)
	}

	if status < 200 || status >= 300 {
		return decodeAPIError(status, data)
	}

	fmt.Print(string(data))
	return nil
}
