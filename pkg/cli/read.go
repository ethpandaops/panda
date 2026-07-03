package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ethpandaops/panda/pkg/serverapi"
)

var readCmd = &cobra.Command{
	GroupID: groupWorkflow,
	Use:     "read <ref>",
	Short:   "Read the full content of a search result by its ref URI",
	Long: `Read the full content of a search result by its ref URI.

Refs are returned by 'panda search' as "Read full content: panda read <ref>".

Supported ref schemes:
  runbooks://{filename-stem}    Full runbook markdown
  eips://{number}               Full EIP content
  consensus-specs://{fork}/{topic}  Full consensus spec document

Examples:
  panda read runbooks://debug_ethereum_network
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
		return formatResourceReadError(ref, status, data)
	}

	fmt.Print(string(data))
	return nil
}

// formatResourceReadError turns a resource-read miss into a path-discovery aid:
// when the server attached candidate URIs, it lists them as copy-paste
// `panda read` hints; otherwise it points to semantic search for finding a
// resource by meaning.
func formatResourceReadError(input string, status int, data []byte) error {
	var payload serverapi.ReadResourceError
	if err := json.Unmarshal(data, &payload); err != nil {
		return decodeAPIError(status, data)
	}

	if len(payload.Candidates) == 0 {
		// No close path matched — search owns discovery "by meaning".
		return fmt.Errorf("%w\n\n  tip: search by meaning — panda search %q", decodeAPIError(status, data), searchHintQuery(input))
	}

	msg := payload.Error
	if msg == "" {
		msg = fmt.Sprintf("no resource matched %q", input)
	}

	var b strings.Builder

	fmt.Fprintf(&b, "%s\n\nNo exact match — did you mean:\n", msg)

	for _, c := range payload.Candidates {
		fmt.Fprintf(&b, "  panda read %s", c.URI)
		if c.Title != "" {
			fmt.Fprintf(&b, "  — %s", c.Title)
		}

		b.WriteByte('\n')
	}

	return errors.New(strings.TrimRight(b.String(), "\n"))
}

// searchHintQuery reduces a failed ref input to plain search terms for the
// "search by meaning" tip (a typo'd "finalty" ref → the search query "finalty").
func searchHintQuery(input string) string {
	var b strings.Builder

	for _, r := range strings.ToLower(input) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte(' ')
		}
	}

	return strings.Join(strings.Fields(b.String()), " ")
}
