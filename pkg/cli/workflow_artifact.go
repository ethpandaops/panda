package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
)

var workflowArtifactOut string

var workflowArtifactCmd = &cobra.Command{
	Use:   "artifact",
	Short: "List and download a run's output artifacts",
	Long: `Artifacts are a run's file outputs, referenced from run outputs as
$resource envelopes. List them to find the concrete row by slotName/mediaType,
then get its bytes.

Examples:
  panda workflow artifact list <wf> <run>
  panda workflow artifact get <wf> <run> <artifactId> --out report.md`,
}

var workflowArtifactListCmd = &cobra.Command{
	Use:   "list <wf> <run>",
	Short: "List a run's artifacts",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		body, err := workflowGet(cmd.Context(), nil, "workflows", args[0], "runs", args[1], "resources")
		if err != nil {
			return err
		}

		return renderWorkflow(body, func(b []byte) {
			summarizeWorkflowItems(b, "items", "No artifacts.")
		})
	},
}

var workflowArtifactGetCmd = &cobra.Command{
	Use:   "get <wf> <run> <artifactId>",
	Short: "Download an artifact's raw bytes",
	Long: `Download an artifact's content verbatim (may be binary). Use --out to
write a file; otherwise the bytes go to stdout (fine for a pipe, but binary
content can garble a TTY).`,
	Args: cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		resp, err := workflowStream(cmd.Context(), "GET", nil, nil, nil,
			workflowPath("workflows", args[0], "runs", args[1], "artifacts", args[2], "content"))
		if err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()

		if workflowArtifactOut == "" || workflowArtifactOut == "-" {
			_, err := io.Copy(os.Stdout, resp.Body)

			return err
		}

		file, err := os.Create(workflowArtifactOut)
		if err != nil {
			return fmt.Errorf("creating %s: %w", workflowArtifactOut, err)
		}

		written, err := io.Copy(file, resp.Body)
		if err != nil {
			_ = file.Close()

			return fmt.Errorf("writing %s: %w", workflowArtifactOut, err)
		}

		// Surface a Close error (e.g. a flush/fsync failure on the final block)
		// instead of reporting success on a possibly-truncated file.
		if err := file.Close(); err != nil {
			return fmt.Errorf("closing %s: %w", workflowArtifactOut, err)
		}

		fmt.Fprintf(os.Stderr, "wrote %d bytes to %s\n", written, workflowArtifactOut)

		return nil
	},
}

func init() {
	workflowArtifactCmd.AddCommand(workflowArtifactListCmd, workflowArtifactGetCmd)

	workflowArtifactGetCmd.Flags().StringVar(&workflowArtifactOut, "out", "",
		"Output file (default: stdout)")

	workflowArtifactListCmd.ValidArgsFunction = noCompletions
	workflowArtifactGetCmd.ValidArgsFunction = noCompletions
}
