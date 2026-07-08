package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
)

var (
	workflowAPIData   string
	workflowAPIFollow bool
)

// workflowDocsTopics maps a `panda workflow docs [topic]` topic to its resource URI.
var workflowDocsTopics = map[string]string{
	"":      "workflow://guide",
	"guide": "workflow://guide",
	"api":   "workflow://api",
}

var workflowDocsCmd = &cobra.Command{
	Use:   "docs [topic]",
	Short: "Show the workflow-engine lifecycle guide and API cheat-sheet",
	Long: `Show the embedded workflow-engine documentation, served from a server resource
(like 'panda docs'). No topic (or 'guide') shows the lifecycle guide; 'api'
shows the endpoint cheat-sheet.

Examples:
  panda workflow docs
  panda workflow docs guide
  panda workflow docs api`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		topic := ""
		if len(args) == 1 {
			topic = args[0]
		}

		uri, ok := workflowDocsTopics[topic]
		if !ok {
			return fmt.Errorf("unknown docs topic %q; valid topics: guide, api", topic)
		}

		response, err := readResource(cmd.Context(), uri)
		if err != nil {
			return err
		}

		if isJSON() {
			return printJSON(response)
		}

		fmt.Print(response.Content)

		return nil
	},
}

var workflowAPICmd = &cobra.Command{
	Use:   "api <METHOD> <path>",
	Short: "Call an uncurated workflow-engine endpoint directly",
	Long: `Call any workflow-engine endpoint through the passthrough. <path> is relative to
/api/v1 (e.g. 'whiteboards' or 'whiteboards/{wb}/state'); a leading '/' or
'/api/v1' is stripped. --data is the request body (inline JSON or @file.json),
passed verbatim. -f streams the response as NDJSON under --json.

Examples:
  panda workflow api GET whiteboards
  panda workflow api GET workflows/{wf}/runs/{run}
  panda workflow api POST dispatch/simulate --data @sim.json
  panda workflow api GET workflows/{wf}/runs/{run}/state/stream -f --json`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		method := strings.ToUpper(args[0])

		segments, query, err := normalizeWorkflowAPIPath(args[1])
		if err != nil {
			return err
		}

		data, err := readInlineOrFile(workflowAPIData)
		if err != nil {
			return err
		}

		if workflowAPIFollow {
			return followWorkflowAPI(commandContext(cmd), method, data, query, segments)
		}

		body, err := workflowSend(cmd.Context(), method, data, nil, query, segments...)
		if err != nil {
			return err
		}

		if isJSON() {
			return printJSONBytes(body)
		}

		summarizeWorkflowObject(body)

		return nil
	},
}

func init() {
	workflowAPICmd.Flags().StringVar(&workflowAPIData, "data", "",
		"Request body as inline JSON or @file.json")
	workflowAPICmd.Flags().BoolVarP(&workflowAPIFollow, "follow", "f", false,
		"Stream the response (SSE) as NDJSON under --json")

	workflowDocsCmd.ValidArgsFunction = func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"guide", "api"}, cobra.ShellCompDirectiveNoFileComp
	}
	workflowAPICmd.ValidArgsFunction = noCompletions
}

// normalizeWorkflowAPIPath splits a raw `api` path into percent-encodable segments
// and an optional query. A leading '/' or a leading 'api/v1' SEGMENT is stripped
// so there is no double '/api/v1' (the engine's base path); 'api/v1foo' is left
// intact. Each segment is passed to workflowPath, which percent-encodes it. A
// malformed query string is surfaced as an error rather than silently dropped.
func normalizeWorkflowAPIPath(raw string) ([]string, url.Values, error) {
	pathPart := raw
	queryPart := ""

	if idx := strings.IndexByte(raw, '?'); idx >= 0 {
		pathPart = raw[:idx]
		queryPart = raw[idx+1:]
	}

	pathPart = strings.TrimPrefix(pathPart, "/")

	// Strip a leading api/v1 only on a full-segment boundary so 'api/v1foo' is
	// not mangled into 'foo'.
	switch {
	case pathPart == "api/v1":
		pathPart = ""
	case strings.HasPrefix(pathPart, "api/v1/"):
		pathPart = strings.TrimPrefix(pathPart, "api/v1/")
	}

	var segments []string

	for _, seg := range strings.Split(pathPart, "/") {
		if seg != "" {
			segments = append(segments, seg)
		}
	}

	var query url.Values

	if queryPart != "" {
		parsed, err := url.ParseQuery(queryPart)
		if err != nil {
			return nil, nil, fmt.Errorf("parsing query %q: %w", queryPart, err)
		}

		query = parsed
	}

	return segments, query, nil
}

// followWorkflowAPI streams a raw workflow endpoint, emitting each SSE data payload as
// NDJSON (under --json) or a text line. Worker-log endpoints frame events as a
// `page` batch, so a data payload with an items[] array is flattened to one
// line per item; other payloads (e.g. state-stream snapshots) pass through
// whole. It runs until EOF or Ctrl-C; there is no terminal-status contract for
// the raw hatch.
func followWorkflowAPI(ctx context.Context, method string, body []byte, query url.Values, segments []string) error {
	resp, err := workflowStream(ctx, method, body, sseHeaders(), query, workflowPath(segments...))
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	streamErr := parseSSE(ctx, resp.Body, emitStreamItems)

	return resolveStreamResult(ctx, streamErr, nil)
}

// completeWorkflowWhiteboardIDs completes the first positional arg with whiteboard
// ids from 'whiteboard list'. Subsequent args are free text (nested ids the CLI
// cannot cheaply enumerate).
func completeWorkflowWhiteboardIDs(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	body, err := workflowGet(commandContext(cmd), nil, "whiteboards")
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	var payload struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}

	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	ids := make([]string, 0, len(payload.Items))
	for _, item := range payload.Items {
		if item.ID != "" {
			ids = append(ids, item.ID)
		}
	}

	return ids, cobra.ShellCompDirectiveNoFileComp
}
