package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/spf13/cobra"
)

// workflowURLCmd mints human-facing workflow-engine frontend links. The web
// origin comes from the server's /api/v1/workflow-info (answered from proxy
// discovery of the engine's web_base_url); no token is involved — users log into
// the workflow-engine UI themselves.
var workflowURLCmd = &cobra.Command{
	Use:   "url [whiteboard <wb> | workflow <wf> | run <wf> <run>]",
	Short: "Print a workflow-engine frontend link (users log in there themselves)",
	Long: `Print a human-facing workflow-engine web UI link. With no arguments, prints
the web origin. Include these links when reporting whiteboards and runs so the
user can open them in a browser — access is their own workflow-engine login,
never the server's or proxy's token.

Examples:
  panda workflow url
  panda workflow url whiteboard <wb>
  panda workflow url workflow <wf>
  panda workflow url run <wf> <run>`,
	Args: cobra.MaximumNArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		base, err := fetchWorkflowWebBase(cmd.Context())
		if err != nil {
			return err
		}

		if base == "" {
			return fmt.Errorf(
				"the server does not expose a workflow-engine web origin — the workflow " +
					"engine is not advertised by any configured proxy (or the server predates " +
					"workflow-info; upgrade panda-server)")
		}

		link, err := buildWorkflowWebURL(base, args)
		if err != nil {
			return err
		}

		if isJSON() {
			return printJSON(map[string]string{"url": link})
		}

		fmt.Println(link)

		return nil
	},
}

// buildWorkflowWebURL maps a `url` argument form onto a frontend path.
func buildWorkflowWebURL(base string, args []string) (string, error) {
	switch {
	case len(args) == 0:
		return base, nil
	case args[0] == "whiteboard" && len(args) == 2:
		return workflowWhiteboardURL(base, args[1]), nil
	case args[0] == "workflow" && len(args) == 2:
		return workflowWorkflowURL(base, args[1]), nil
	case args[0] == "run" && len(args) == 3:
		return workflowRunURL(base, args[1], args[2]), nil
	default:
		return "", fmt.Errorf(
			"usage: url [whiteboard <wb> | workflow <wf> | run <wf> <run>], got %q",
			strings.Join(args, " "))
	}
}

// workflowWhiteboardURL builds the frontend link for a whiteboard.
func workflowWhiteboardURL(base, wb string) string {
	return base + "/whiteboards/" + wb
}

// workflowWorkflowURL builds the frontend link for a workflow.
func workflowWorkflowURL(base, wf string) string {
	return base + "/workflows/" + wf
}

// workflowRunURL builds the frontend link for a workflow run.
func workflowRunURL(base, wf, run string) string {
	return base + "/workflows/" + wf + "/runs/" + run
}

// fetchWorkflowWebBase reads the workflow-engine web origin from the server's
// /api/v1/workflow-info. It returns "" (no error) when the engine is not
// advertised or the server predates the endpoint (404), so callers can degrade
// to link-less output; transport errors still propagate.
func fetchWorkflowWebBase(ctx context.Context) (string, error) {
	data, status, _, err := serverDo(ctx, http.MethodGet, "/api/v1/workflow-info", nil, nil, nil)
	if err != nil {
		return "", err
	}

	if status == http.StatusNotFound {
		return "", nil
	}

	if status < 200 || status >= 300 {
		return "", workflowError(status, data)
	}

	var payload struct {
		Enabled    bool   `json:"enabled"`
		WebBaseURL string `json:"web_base_url"`
	}

	if err := json.Unmarshal(data, &payload); err != nil {
		return "", fmt.Errorf("parsing workflow-info: %w", err)
	}

	if !payload.Enabled {
		return "", nil
	}

	return strings.TrimRight(payload.WebBaseURL, "/"), nil
}

// workflowWebBaseBestEffort resolves the web origin for optional link decoration,
// swallowing every failure into "" — callers render link-less output rather
// than fail their primary job over a missing frontend origin.
func workflowWebBaseBestEffort(ctx context.Context) string {
	base, err := fetchWorkflowWebBase(ctx)
	if err != nil {
		return ""
	}

	return base
}
