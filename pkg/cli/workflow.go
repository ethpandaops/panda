package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

const workflowLong = `Author, run, and monitor workflows on the workflow engine.

The workflow engine is an external service that designs, publishes, and runs
multi-step agent workflows. panda talks to it through the local server, which
relays to the panda proxy; the proxy holds the credential and the CLI never
sees it. Access is configured on the proxy, not on the server.

Resource model (you drive the sequence, the CLI does not orchestrate):

  whiteboard  ── the planning space (holds sessions + drafts)     wb_…
    └─ session ── your chat with the worker that writes drafts     ses_…
         └─ draft ── a candidate workflow spec (iterate → revise)
              └─ publish ─▶ workflow ── the executable object      wf_…
                                └─ run ── one execution             run_…

The lifecycle is whiteboard → session → draft → workflow → run.

This command group exposes raw CRUD + streaming primitives, one workflow-engine
REST call per leaf command. It is not the whiteboard skill: the design → publish
→ run → follow sequence is yours to drive, and the workflow engine owns drafting
— describe what you want in plain language and let it write the spec.

Agents: publishing/running is a side-effect boundary, and FRESH IS THE DEFAULT
— a new request gets a new whiteboard → draft → review → run. Never hunt
through 'workflow list' / 'whiteboard list' for an existing item to run or
continue; reuse only what the user explicitly named. Render the draft for your
user with 'draft show' and get their explicit publish/run approval first — the
original task request alone is not approval. 'draft publish', 'draft run', and
'run create' enforce a tripwire (--approved, re-typing the reviewed id); the
flag is proof of review, never a substitute for the user's approval. The full
operator loop, checkpoint choices, and approval rules are in 'panda workflow
docs'.

The run-stream status→exit contract applies only to run streams: 'run watch' and
'run logs -f' set the exit code from the terminal run.status (0 on completed,
non-zero on failed/cancelled). Session and whiteboard streams exit non-zero only
on a stream/transport error.

Read 'panda workflow docs' for the lifecycle and examples.

Examples:
  panda workflow whiteboard create --requirements "count blocks per day" --json
  panda workflow session create <wb> --content "count blocks per day" --json
  panda workflow draft show <wb> <draft>
  panda workflow draft run <wb> <draft> --approved <draft> --json
  panda workflow run watch <wf> <run> --json
  panda workflow docs`

var workflowCmd = &cobra.Command{
	GroupID: groupWorkflow,
	Use:     "workflow",
	Aliases: []string{"wf"},
	Short:   "Author, run, and monitor workflows on the workflow engine",
	Long:    workflowLong,
}

func init() {
	rootCmd.AddCommand(workflowCmd)
	workflowCmd.AddCommand(
		// Base workflow-noun verbs live directly on `panda workflow`.
		workflowListCmd,
		workflowGetCmd,
		workflowReleaseCmd,
		workflowWhiteboardCmd,
		workflowSessionCmd,
		workflowDraftCmd,
		workflowRunCmd,
		workflowSteerCmd,
		workflowArtifactCmd,
		workflowDispatchCmd,
		workflowURLCmd,
		workflowDocsCmd,
		workflowAPICmd,
	)
}

// workflowPath builds a `/api/v1/workflow/<segments>` passthrough path, percent-
// encoding each segment so IDs carrying reserved characters (e.g. a loop steer
// key like `tasks.fetch.one[iter=0002]`) survive intact and are never
// concatenated raw. url.PathEscape encodes `[` and `]` but leaves `=` literal,
// so we additionally encode `=`→`%3D` for spec fidelity (`[ ] =`).
func workflowPath(segments ...string) string {
	var b strings.Builder

	b.WriteString("/api/v1/workflow")

	for _, seg := range segments {
		b.WriteByte('/')
		b.WriteString(strings.ReplaceAll(url.PathEscape(seg), "=", "%3D"))
	}

	return b.String()
}

// workflowAPIError is a non-2xx workflow passthrough response. Its hint is keyed on
// the HTTP status and overrides the generic serverErrorHint (whose 503 text is
// sandbox-specific and wrong for the workflow engine).
type workflowAPIError struct {
	Status  int
	Message string
}

func (e *workflowAPIError) Error() string {
	if hint := workflowErrorHint(e.Status, e.Message); hint != "" {
		return fmt.Sprintf("HTTP %d: %s\n\n  hint: %s", e.Status, e.Message, hint)
	}

	return fmt.Sprintf("HTTP %d: %s", e.Status, e.Message)
}

// workflowErrorHint returns a workflow-engine-specific hint for the statuses
// where the generic hint would be misleading or absent. It reuses
// apiErrorMessage's decoded body via the caller, so problem+json and text/plain
// bodies both render.
//
// The hints are keyed on the HTTP status plus the canonical problem+json detail
// substrings emitted along the panda → server → proxy → workflow-engine path, so
// a proxy-not-advertising 503, a proxy-unreachable 502, and an engine-unreachable
// 502 are each named precisely rather than collapsed into one message. A body
// without a recognized phrase (a verbatim upstream 5xx page, an engine 401) falls
// through to the upstream-origin wording.
func workflowErrorHint(status int, message string) string {
	lower := strings.ToLower(message)

	switch status {
	case http.StatusServiceUnavailable:
		// Only panda-server's "no proxy advertises the engine" short-circuit is a
		// local-config problem; any other 503 is a forwarded upstream outage.
		if strings.Contains(lower, "no configured proxy advertises") {
			return "the workflow engine is not enabled on any configured proxy — " +
				"add the workflow block to your proxy's proxy-config.yaml (or ask the proxy operator)"
		}

		return "the workflow engine returned a server error upstream — retry; " +
			"if it persists contact the operator"
	case http.StatusBadGateway:
		// The server emits "proxy is unreachable"; the proxy emits "configured but
		// unreachable" for a dead engine. Distinguish the two hops; a 502 body with
		// neither phrase was relayed verbatim from beyond the proxy, so blame the
		// engine side rather than either hop.
		if strings.Contains(lower, "proxy is unreachable") {
			return "panda could not reach the proxy — check the server's proxies[] " +
				"config and that the proxy is up"
		}

		if strings.Contains(lower, "configured but unreachable") {
			return "the proxy could not reach the workflow engine — the upstream may be " +
				"down; try again or contact the operator"
		}

		return "the workflow engine returned a server error upstream — retry; " +
			"if it persists contact the operator"
	case http.StatusUnauthorized:
		// A proxy problem+json 401 (the proxy rejected the caller's credential)
		// mentions the proxy; an engine 401 body is relayed verbatim and does not.
		if strings.Contains(lower, "proxy") {
			return "the proxy rejected your credential — run 'panda auth login' and retry"
		}

		return "the workflow engine rejected the credential — if your proxy uses " +
			"passthrough auth, re-run 'panda auth login' (your token may predate the " +
			"engine scope); otherwise ask the proxy operator to check the configured api_token"
	case http.StatusForbidden:
		return "your account is not allowed to use the workflow engine on this proxy — " +
			"ask an operator for access"
	case http.StatusNotFound:
		return "check the whiteboard / run / draft id (or the path for " +
			"`panda workflow api`)"
	default:
		// Any other upstream 5xx (500, 504, …): an engine-side error, not a local
		// misconfig — say so rather than leaving it hintless.
		if status >= 500 {
			return "the workflow engine returned a server error upstream — retry; " +
				"if it persists contact the operator"
		}

		return ""
	}
}

// workflowError wraps a non-2xx passthrough response, decoding its body via the
// shared apiErrorMessage fallback chain (error → detail → title → raw body).
func workflowError(status int, data []byte) error {
	return &workflowAPIError{Status: status, Message: apiErrorMessage(data)}
}

// workflowGet performs a GET against the workflow passthrough and returns the raw
// response body, routing non-2xx through the workflow-hinted error.
func workflowGet(ctx context.Context, query url.Values, segments ...string) ([]byte, error) {
	data, status, _, err := serverDo(ctx, http.MethodGet, workflowPath(segments...), nil, query, nil)
	if err != nil {
		return nil, err
	}

	if status < 200 || status >= 300 {
		return nil, workflowError(status, data)
	}

	return data, nil
}

// workflowSend performs a write (POST/PATCH/DELETE) against the workflow passthrough
// and returns the raw response body. A nil body sends no payload; a non-nil
// body sets Content-Type: application/json (the engine rejects a bare form body).
// It tolerates an empty 2xx body (e.g. run cancel's 204) — it never unmarshals.
func workflowSend(
	ctx context.Context,
	method string,
	body []byte,
	headers map[string]string,
	query url.Values,
	segments ...string,
) ([]byte, error) {
	var reader io.Reader

	sendHeaders := make(map[string]string, len(headers)+1)
	maps.Copy(sendHeaders, headers)

	if body != nil {
		reader = bytes.NewReader(body)
		sendHeaders["Content-Type"] = "application/json"
	}

	data, status, _, err := serverDo(ctx, method, workflowPath(segments...), reader, query, sendHeaders)
	if err != nil {
		return nil, err
	}

	if status < 200 || status >= 300 {
		return nil, workflowError(status, data)
	}

	return data, nil
}

// workflowStream opens a streaming request against the workflow passthrough and
// returns the OPEN response (the caller MUST close its Body) for SSE and
// artifact downloads. It checks the HTTP status BEFORE returning: on a non-2xx
// it reads the small error body, closes it, and returns a workflow-hinted error,
// so an error body is never fed to the SSE parser. A non-nil body sets
// Content-Type: application/json.
func workflowStream(
	ctx context.Context,
	method string,
	body []byte,
	headers map[string]string,
	query url.Values,
	rawPath string,
) (*http.Response, error) {
	baseURL, err := serverBaseURL()
	if err != nil {
		return nil, err
	}

	reqURL := strings.TrimRight(baseURL, "/") + rawPath
	if len(query) > 0 {
		reqURL += "?" + query.Encode()
	}

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, reqURL, reader)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	// Streaming requests carry the same caller attribution as buffered serverDo
	// requests, so the proxy's audit log attributes watch/follow/log streams too.
	applyEnvAttribution(req.Header)

	resp, err := serverHTTP.Do(req)
	if err != nil {
		if isConnectionRefused(err) {
			return nil, fmt.Errorf(
				"server is not running at %s — run 'panda init' or 'panda server start' first",
				baseURL,
			)
		}

		return nil, fmt.Errorf("request failed: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()

		return nil, workflowError(resp.StatusCode, data)
	}

	return resp, nil
}

// maxSSELineBytes bounds a single SSE line (a `data:`/`event:`/`id:` line, or a
// blank-line frame delimiter). It guards against a newline-sparse body — e.g. a
// large base64 artifact mis-routed through `api -f` — being buffered unbounded
// into memory. It is far above any legitimate SSE line (worker-log pages and
// state snapshots are well under a MiB) yet a hard ceiling against OOM/DoS.
const maxSSELineBytes = 8 << 20 // 8 MiB

// sseEvent is one parsed server-sent event frame.
type sseEvent struct {
	Event string
	ID    string
	Data  string
}

// parseSSE reads server-sent event frames from r and invokes fn once per
// complete (blank-line-delimited) frame. It:
//   - skips `:`-comment lines (the engine's `: ping` heartbeat every 30s),
//   - does NOT surface an `event: sync` frame (replay-caught-up marker) as a
//     data event or turn-done — such frames are dropped entirely,
//   - joins multiple `data:` lines in a frame with "\n",
//   - uses bufio.Reader.ReadString rather than bufio.Scanner to avoid the 64K
//     token cap on large payloads.
//
// It returns nil at end of stream (io.EOF), fn's error if fn returns one, or a
// wrapped read/context error. bufio transparently reassembles reads that split
// mid-frame.
func parseSSE(ctx context.Context, r io.Reader, fn func(sseEvent) error) error {
	reader := bufio.NewReader(r)

	var (
		event     string
		id        string
		dataLines []string
		haveData  bool
	)

	flush := func() error {
		defer func() { event, id, dataLines, haveData = "", "", nil, false }()

		if event == "sync" {
			return nil
		}

		if !haveData && event == "" && id == "" {
			return nil
		}

		return fn(sseEvent{Event: event, ID: id, Data: strings.Join(dataLines, "\n")})
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		line, readErr := readBoundedSSELine(reader, maxSSELineBytes)

		if len(line) > 0 {
			trimmed := strings.TrimRight(line, "\n")
			trimmed = strings.TrimRight(trimmed, "\r")

			switch {
			case trimmed == "":
				if err := flush(); err != nil {
					return err
				}
			case strings.HasPrefix(trimmed, ":"):
				// Comment / heartbeat line — skip.
			default:
				field, value, _ := strings.Cut(trimmed, ":")
				value = strings.TrimPrefix(value, " ")

				switch field {
				case "event":
					event = value
				case "id":
					id = value
				case "data":
					dataLines = append(dataLines, value)
					haveData = true
				}
			}
		}

		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return flush()
			}

			return fmt.Errorf("reading stream: %w", readErr)
		}
	}
}

// readBoundedSSELine reads up to and including the next '\n' from r, returning
// an error once the accumulated line exceeds maxBytes rather than buffering an
// unbounded newline-sparse payload. It uses ReadSlice so the ceiling is enforced
// incrementally (a few KiB at a time) instead of after the whole line is in
// memory. The returned line retains its trailing '\n' (as ReadString did).
func readBoundedSSELine(r *bufio.Reader, maxBytes int) (string, error) {
	var b strings.Builder

	for {
		chunk, err := r.ReadSlice('\n')

		if b.Len()+len(chunk) > maxBytes {
			return "", fmt.Errorf("stream line exceeds %d bytes", maxBytes)
		}

		b.Write(chunk)

		switch {
		case err == nil:
			return b.String(), nil
		case errors.Is(err, bufio.ErrBufferFull):
			// Line longer than the bufio buffer: keep accumulating, bounded by
			// the maxBytes check above.
			continue
		default:
			return b.String(), err
		}
	}
}

// printNDJSONBytes compacts a JSON payload to a single line and prints it, for
// NDJSON stream output. A non-JSON payload is printed verbatim (trimmed).
func printNDJSONBytes(data []byte) error {
	var buf bytes.Buffer
	if err := json.Compact(&buf, data); err != nil {
		fmt.Println(strings.TrimSpace(string(data)))

		return nil
	}

	buf.WriteByte('\n')
	_, err := buf.WriteTo(os.Stdout)

	return err
}

// readInlineOrFile resolves a flag value that is either inline JSON/text or a
// `@path` file reference, returning the bytes verbatim (no validation). An
// empty value returns (nil, nil).
func readInlineOrFile(value string) ([]byte, error) {
	if value == "" {
		return nil, nil
	}

	if strings.HasPrefix(value, "@") {
		path := value[1:]

		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}

		return data, nil
	}

	return []byte(value), nil
}

// readJSONFlag resolves an inline-or-@file flag whose payload gets embedded
// verbatim (json.RawMessage) into a request body, validating it up front so a
// typo surfaces as "--inputs is not valid JSON" instead of a cryptic
// json.RawMessage marshal error. An empty value returns (nil, nil).
func readJSONFlag(flag, value string) ([]byte, error) {
	data, err := readInlineOrFile(value)
	if err != nil || len(data) == 0 {
		return data, err
	}

	if !json.Valid(data) {
		return nil, fmt.Errorf("%s is not valid JSON", flag)
	}

	return data, nil
}

// renderWorkflow prints a raw workflow response body: pretty raw JSON under --json
// (preserving number precision), otherwise the caller's text summary.
func renderWorkflow(body []byte, summarize func([]byte)) error {
	if isJSON() {
		return printJSONBytes(body)
	}

	summarize(body)

	return nil
}

// summarizeWorkflowItems prints a concise, non-contractual listing of `body[key]`,
// one line per item with any of id/name/status/revision present. It falls back
// to compact JSON when the shape is unexpected. --json is the stable contract.
func summarizeWorkflowItems(body []byte, key, empty string) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		fmt.Println(strings.TrimSpace(string(body)))

		return
	}

	raw, ok := payload[key]
	if !ok {
		summarizeWorkflowObject(body)

		return
	}

	var items []map[string]any
	if err := json.Unmarshal(raw, &items); err != nil {
		_ = printNDJSONBytes(raw)

		return
	}

	if len(items) == 0 {
		fmt.Println(empty)

		return
	}

	rows := make([][]string, 0, len(items))
	identified := false

	for _, item := range items {
		id := stringField(item, "id")
		name := stringField(item, "name")
		status := stringField(item, "status")

		if id != "-" || name != "-" || status != "-" {
			identified = true
		}

		rows = append(rows, []string{id, name, status})
	}

	// Lists whose items carry none of id/name/status (session/steer turns,
	// dispatch inventory entries) would render as a block of pure dashes. Fall
	// back to a compact scalar summary per item instead of the id/name/status
	// table. --json remains the stable contract.
	if !identified {
		for _, item := range items {
			fmt.Println(scalarFieldLine(item))
		}

		return
	}

	printTable([]string{"ID", "NAME", "STATUS"}, rows)
}

// scalarFieldLine renders an item's scalar fields as a single sorted
// `key=value  key=value` line, skipping nested arrays/objects (e.g. a turn's
// large embedded events[]) and nil values. It is the compact per-item fallback
// for id-less lists in text mode.
func scalarFieldLine(item map[string]any) string {
	keys := make([]string, 0, len(item))

	for k, v := range item {
		switch v.(type) {
		case nil, map[string]any, []any:
			// Skip nils and nested structures — --json shows them.
		default:
			keys = append(keys, k)
		}
	}

	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, item[k]))
	}

	return strings.Join(parts, "  ")
}

// summarizeWorkflowObject prints the top-level scalar fields of a JSON object as
// aligned key/value pairs — a non-contractual convenience for text mode.
func summarizeWorkflowObject(body []byte) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		fmt.Println(strings.TrimSpace(string(body)))

		return
	}

	keys := make([]string, 0, len(payload))
	for k := range payload {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	pairs := make([][2]string, 0, len(keys))

	for _, k := range keys {
		switch v := payload[k].(type) {
		case map[string]any, []any:
			// Skip nested structures in the summary; --json shows them.
		default:
			pairs = append(pairs, [2]string{k, fmt.Sprint(v)})
		}
	}

	if len(pairs) == 0 {
		fmt.Println("(no scalar fields; use --json for the full object)")

		return
	}

	printKeyValue(pairs)
}

// stringField returns m[key] as a string, or "-" when absent/non-string.
func stringField(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok && v != "" {
		return v
	}

	return "-"
}

// errStreamComplete is a sentinel returned from an SSE callback to stop the
// stream cleanly once a client-side terminal condition is reached.
var errStreamComplete = errors.New("stream complete")

// sseHeaders returns the headers for opening an SSE stream. The passthrough
// allow-lists Accept and Last-Event-ID; the workflow-engine credential is
// injected by the proxy, never by the CLI.
func sseHeaders() map[string]string {
	return map[string]string{"Accept": "text/event-stream"}
}

// emitStreamData emits one SSE data payload: a compact NDJSON line under --json,
// else a concise, non-contractual text line. Empty data is dropped.
func emitStreamData(ev sseEvent) error {
	if ev.Data == "" {
		return nil
	}

	if isJSON() {
		return printNDJSONBytes([]byte(ev.Data))
	}

	if ev.Event != "" {
		fmt.Printf("%s\t%s\n", ev.Event, ev.Data)
	} else {
		fmt.Println(ev.Data)
	}

	return nil
}

// workerLogPage is the data payload of a worker-log SSE `page` frame: a batch
// of log items plus a resume cursor. The worker-log stream frames EVERY event
// as `event: page`; the real per-event type lives inside each item's `.type`.
type workerLogPage struct {
	Items      []json.RawMessage `json:"items"`
	LiveCursor string            `json:"liveCursor"`
}

// workerLogItem is one entry inside a worker-log page's items[] batch. Only the
// fields the CLI renders in text mode are declared; --json emits the raw item.
type workerLogItem struct {
	Type      string `json:"type"`
	EventName string `json:"eventName"`
	Message   string `json:"message"`
}

// emitStreamItems emits one SSE data frame, flattening a worker-log page into
// one line per items[] element (each raw item as its own NDJSON line under
// --json, else a concise text line). A payload that is not a page with an
// items[] array falls back to emitting the whole data payload (state streams,
// which carry a single snapshot object per frame, pass through unchanged).
func emitStreamItems(ev sseEvent) error {
	if ev.Data == "" {
		return nil
	}

	var page workerLogPage
	if err := json.Unmarshal([]byte(ev.Data), &page); err != nil || page.Items == nil {
		return emitStreamData(ev)
	}

	for _, raw := range page.Items {
		if err := emitWorkerLogItem(raw); err != nil {
			return err
		}
	}

	return nil
}

// emitWorkerLogItem emits a single worker-log item: a compact NDJSON line under
// --json, else a concise `type\tmessage` text line. A shape the CLI does not
// recognize falls back to a compact JSON line.
func emitWorkerLogItem(raw json.RawMessage) error {
	if isJSON() {
		return printNDJSONBytes(raw)
	}

	var item workerLogItem
	if err := json.Unmarshal(raw, &item); err != nil {
		fmt.Println(strings.TrimSpace(string(raw)))

		return nil
	}

	return printWorkerLogText(item, raw)
}

// printWorkerLogText renders one worker-log item as a concise text line:
// `type\tmessage`, or whichever of the two is present, falling back to a
// compact JSON line when neither is. Shared by the streamed (`-f`) and the
// non-streamed history renderings so both present identically.
func printWorkerLogText(item workerLogItem, raw json.RawMessage) error {
	switch {
	case item.Type != "" && item.Message != "":
		fmt.Printf("%s\t%s\n", item.Type, item.Message)
	case item.Type != "":
		fmt.Println(item.Type)
	case item.Message != "":
		fmt.Println(item.Message)
	default:
		return printNDJSONBytes(raw)
	}

	return nil
}

// summarizeWorkerLog renders a worker-log history page (`{items:[…]}`) as one
// concise `type\tmessage` line per event — matching the `-f` stream rendering,
// so the follow and non-follow log paths present identical text (the generic
// id/name/status table would render worker-log events, which carry neither, as
// a useless block of dashes). It falls back to the raw body on an unexpected
// shape and to the empty message on an empty page.
func summarizeWorkerLog(body []byte, empty string) {
	var payload struct {
		Items []json.RawMessage `json:"items"`
	}

	if err := json.Unmarshal(body, &payload); err != nil {
		fmt.Println(strings.TrimSpace(string(body)))

		return
	}

	if len(payload.Items) == 0 {
		fmt.Println(empty)

		return
	}

	for _, raw := range payload.Items {
		var item workerLogItem
		if err := json.Unmarshal(raw, &item); err != nil {
			fmt.Println(strings.TrimSpace(string(raw)))

			continue
		}

		_ = printWorkerLogText(item, raw)
	}
}

// emitSnapshot emits a refetched /state snapshot: a compact NDJSON line under
// --json (NDJSON of snapshots), else a key/value summary.
func emitSnapshot(body []byte) error {
	if isJSON() {
		return printNDJSONBytes(body)
	}

	summarizeWorkflowObject(body)

	return nil
}

// resolveStreamResult maps a completed stream into a command result. A
// client-side terminal (errStreamComplete) returns termErr (an exit-code error
// or nil). A user interrupt (ctx cancelled) exits cleanly. Otherwise a genuine
// stream/transport error propagates; a plain EOF (streamErr nil) exits cleanly.
func resolveStreamResult(ctx context.Context, streamErr, termErr error) error {
	if errors.Is(streamErr, errStreamComplete) {
		return termErr
	}

	if ctx.Err() != nil {
		return nil
	}

	if streamErr != nil && !errors.Is(streamErr, context.Canceled) {
		return streamErr
	}

	return nil
}

// terminalRunStatuses is the set of terminal workflow run.status values.
var terminalRunStatuses = map[string]struct{}{
	"completed": {},
	"failed":    {},
	"cancelled": {},
}

// isTerminalRunStatus reports whether a run.status is terminal.
func isTerminalRunStatus(status string) bool {
	_, ok := terminalRunStatuses[status]

	return ok
}

// runStatusExitError maps a terminal run.status to a command result: nil (exit
// 0) on completed, a code-1 exitCodeError on failed/cancelled, so a harness can
// branch on $?.
func runStatusExitError(status string) error {
	if status == "completed" {
		return nil
	}

	return &exitCodeError{code: 1}
}
