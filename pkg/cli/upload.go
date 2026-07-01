package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
)

var (
	uploadName   string
	uploadPublic bool
	uploadNoOpen bool
)

var uploadCmd = &cobra.Command{
	GroupID: groupDirect,
	Use:     "upload <file>...",
	Short:   "Upload file(s); private preview by default, publish with a click",
	Long: `Upload one or more files. Each is kept private in the local server (in memory,
for this session only) and opens a preview page where you can click "Make public"
to get a durable, shareable URL. Nothing leaves your machine until you publish.

Use --public to skip the preview and publish immediately (for scripts/CI).
Use "-" to read from stdin (requires --name for the extension).

Examples:
  panda upload report.html
  panda upload chart.png --public
  cat gas.svg | panda upload - --name gas.svg`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		for _, arg := range args {
			if err := uploadOne(cmd, arg, len(args) == 1); err != nil {
				return err
			}
		}

		return nil
	},
}

type uploadStored struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	PreviewPath string `json:"preview_path"`
}

func uploadOne(cmd *cobra.Command, arg string, single bool) error {
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

	var stored uploadStored
	if err := json.Unmarshal(data, &stored); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}

	if uploadPublic {
		return publishUpload(cmd, stored.ID)
	}

	base, err := serverBaseURL()
	if err != nil {
		return err
	}

	previewURL := strings.TrimRight(base, "/") + stored.PreviewPath

	if isJSON() {
		return printJSON(map[string]string{
			"id":           stored.ID,
			"name":         stored.Name,
			"content_type": stored.ContentType,
			"preview_url":  previewURL,
		})
	}

	fmt.Println(previewURL)
	fmt.Fprintln(os.Stderr, "private preview (session-only) — click \"Make public\" to publish")

	if single && !uploadNoOpen {
		_ = openBrowser(previewURL)
	}

	return nil
}

func publishUpload(cmd *cobra.Command, id string) error {
	reqBody, err := json.Marshal(map[string]string{"id": id})
	if err != nil {
		return err
	}

	data, status, _, err := serverDo(commandContext(cmd), http.MethodPost, "/api/v1/uploads/publish",
		bytes.NewReader(reqBody), nil, map[string]string{"Content-Type": "application/json"})
	if err != nil {
		return err
	}

	if status < 200 || status >= 300 {
		return decodeAPIError(status, data)
	}

	if isJSON() {
		return printJSONBytes(data)
	}

	var result struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}

	fmt.Println(result.URL)

	return nil
}

// openBrowser best-effort opens a URL in the default browser; errors are ignored
// by callers (e.g. headless environments).
func openBrowser(target string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", target).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", target).Start()
	default:
		return exec.Command("xdg-open", target).Start()
	}
}

func init() {
	rootCmd.AddCommand(uploadCmd)
	uploadCmd.Flags().StringVar(&uploadName, "name", "",
		"Object filename; its extension sets Content-Type (required for stdin)")
	uploadCmd.Flags().BoolVar(&uploadPublic, "public", false,
		"Publish immediately and print the public URL, skipping the preview")
	uploadCmd.Flags().BoolVar(&uploadNoOpen, "no-open", false,
		"Do not open the preview page in a browser")
}
