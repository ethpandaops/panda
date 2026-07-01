package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var uploadName string

var uploadCmd = &cobra.Command{
	GroupID: groupDirect,
	Use:     "upload <file>...",
	Short:   "Upload file(s) and print a durable, shareable public URL",
	Long: `Upload one or more files to the ethpandaops object store and print a
durable public URL for each. Credentials stay in the proxy; the CLI only streams
bytes to the server.

Use "-" to read from stdin (requires --name for the extension).

Examples:
  panda upload timings.png
  panda upload report.md --json
  cat gas.svg | panda upload - --name gas.svg`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		for _, arg := range args {
			if err := uploadOne(cmd, arg); err != nil {
				return err
			}
		}

		return nil
	},
}

type uploadResult struct {
	URL         string `json:"url"`
	Key         string `json:"key"`
	Size        int64  `json:"size"`
	ContentType string `json:"content_type"`
}

func uploadOne(cmd *cobra.Command, arg string) error {
	var body io.Reader

	name := uploadName

	if arg == "-" {
		if name == "" {
			return fmt.Errorf("--name is required when reading from stdin")
		}

		body = cmd.InOrStdin()
	} else {
		f, err := os.Open(arg)
		if err != nil {
			return fmt.Errorf("opening %s: %w", arg, err)
		}
		defer func() { _ = f.Close() }()

		body = f
		if name == "" {
			name = filepath.Base(arg)
		}
	}

	query := url.Values{}
	query.Set("name", name)

	data, status, _, err := serverDo(commandContext(cmd), http.MethodPost, "/api/v1/uploads", body, query, nil)
	if err != nil {
		return err
	}

	if status < 200 || status >= 300 {
		return decodeAPIError(status, data)
	}

	if isJSON() {
		return printJSONBytes(data)
	}

	var result uploadResult
	if err := json.Unmarshal(data, &result); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}

	fmt.Println(result.URL)
	fmt.Fprintf(os.Stderr, "uploaded %s (%d bytes)\n", name, result.Size)

	return nil
}

func init() {
	rootCmd.AddCommand(uploadCmd)
	uploadCmd.Flags().StringVar(&uploadName, "name", "",
		"Object filename; its extension sets Content-Type (required for stdin)")
}
