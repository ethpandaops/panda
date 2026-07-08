package cli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// workflowDraftShowCmd renders the human-facing review of a draft — the plan
// presented at the review checkpoint before publish/run approval: `draft show`
// renders what would run, `draft run --approved` applies it.
var workflowDraftShowCmd = &cobra.Command{
	Use:   "show <wb> <draft>",
	Short: "Render a draft review for the user (DAG, inputs, outputs)",
	Long: `Render the human-facing review of a draft: id/revision/status, declared
inputs (with defaults), scalar and artifact outputs, and the task DAG in
dependency order. This is the review to present at the publish/run checkpoint
(see 'panda workflow docs') — show it to the user, then ask for approval.

Under --json it emits the structured review (parsed from the draft's graph and
compiledSpecJson) rather than the raw draft object; use 'draft get' for that.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		body, err := workflowGet(cmd.Context(), nil, "whiteboards", args[0], "drafts", args[1])
		if err != nil {
			return err
		}

		review, err := buildDraftReview(body)
		if err != nil {
			return err
		}

		// Best-effort frontend link so the review hands the user something
		// clickable; a missing web origin degrades to a link-less review.
		if base := workflowWebBaseBestEffort(cmd.Context()); base != "" {
			review.WhiteboardURL = workflowWhiteboardURL(base, args[0])
		}

		if isJSON() {
			return printJSON(review)
		}

		renderDraftReviewText(review, args[0])

		return nil
	},
}

// draftReviewPort is one declared input or output slot in a draft review.
type draftReviewPort struct {
	Name        string `json:"name"`
	Type        string `json:"type,omitempty"`
	Default     any    `json:"default,omitempty"`
	HasDefault  bool   `json:"hasDefault,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Description string `json:"description,omitempty"`
	ContentType string `json:"contentType,omitempty"`
}

// draftReviewPorts groups a draft's declared slots by section.
type draftReviewPorts struct {
	Values    []draftReviewPort `json:"values,omitempty"`
	Artifacts []draftReviewPort `json:"artifacts,omitempty"`
	Secrets   []draftReviewPort `json:"secrets,omitempty"`
}

// draftReviewNode is one DAG node in a draft review, in dependency order.
type draftReviewNode struct {
	ID          string   `json:"id"`
	Kind        string   `json:"kind,omitempty"`
	Needs       []string `json:"needs,omitempty"`
	QualityGate bool     `json:"qualityGate,omitempty"`
	HasRetry    bool     `json:"hasRetry,omitempty"`
}

// draftReview is the structured review of a draft: the reviewable surface of
// the workflow (identity, declared IO, DAG) without the embedded spec blobs.
type draftReview struct {
	ID       string            `json:"id"`
	Revision any               `json:"revision,omitempty"`
	Status   string            `json:"status,omitempty"`
	Inputs   draftReviewPorts  `json:"inputs"`
	Outputs  draftReviewPorts  `json:"outputs"`
	DAG      []draftReviewNode `json:"dag"`
	// SpecNote flags a missing/unparseable compiledSpecJson so the reviewer
	// knows the inputs/outputs sections are absent rather than empty.
	SpecNote string `json:"specNote,omitempty"`
	// WhiteboardURL is the frontend link for the draft's whiteboard, when the
	// server exposes workflow's web origin. Users log in there themselves.
	WhiteboardURL string `json:"whiteboardUrl,omitempty"`
}

// compiledSpecPort is one slot spec inside a compiled spec's inputs/outputs
// sections. Only the review-relevant fields are declared.
type compiledSpecPort struct {
	Description string `json:"description"`
	Required    bool   `json:"required"`
	ContentType string `json:"contentType"`
	Schema      struct {
		Type    string          `json:"type"`
		Default json.RawMessage `json:"default"`
	} `json:"schema"`
}

// buildDraftReview parses a raw draft object into a structured review. The DAG
// comes from `.graph.nodes[]`; declared inputs/outputs come from
// `.compiledSpecJson` (a JSON string, parsed here so callers never touch it). A
// missing or unparseable compiled spec degrades to a SpecNote instead of
// failing — the DAG alone is still reviewable.
func buildDraftReview(body []byte) (*draftReview, error) {
	var raw struct {
		ID       string `json:"id"`
		Revision any    `json:"revision"`
		Status   string `json:"status"`
		Graph    struct {
			Nodes []struct {
				ID          string   `json:"id"`
				Name        string   `json:"name"`
				Kind        string   `json:"kind"`
				Needs       []string `json:"needs"`
				QualityGate any      `json:"qualityGate"`
				HasRetry    bool     `json:"hasRetry"`
			} `json:"nodes"`
		} `json:"graph"`
		CompiledSpecJSON string `json:"compiledSpecJson"`
	}

	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parsing draft: %w", err)
	}

	if raw.ID == "" {
		return nil, fmt.Errorf("response carries no draft id — is this a draft object? (use 'draft list' to find one)")
	}

	review := &draftReview{ID: raw.ID, Revision: raw.Revision, Status: raw.Status}

	nodes := make([]draftReviewNode, 0, len(raw.Graph.Nodes))
	for _, n := range raw.Graph.Nodes {
		nodes = append(nodes, draftReviewNode{
			ID:          n.ID,
			Kind:        n.Kind,
			Needs:       n.Needs,
			QualityGate: truthyFlag(n.QualityGate),
			HasRetry:    n.HasRetry,
		})
	}

	review.DAG = topoSortReviewNodes(nodes)

	if err := parseCompiledSpecPorts(raw.CompiledSpecJSON, review); err != nil {
		review.SpecNote = fmt.Sprintf(
			"compiledSpecJson missing or unparseable (%v); declared inputs/outputs not shown", err)
	}

	return review, nil
}

// parseCompiledSpecPorts fills review.Inputs/Outputs from a compiled spec JSON
// string (`{inputs:{values,artifacts,secrets}, outputs:{…}}`).
func parseCompiledSpecPorts(compiled string, review *draftReview) error {
	if strings.TrimSpace(compiled) == "" {
		return fmt.Errorf("empty")
	}

	var spec struct {
		Inputs  map[string]map[string]compiledSpecPort `json:"inputs"`
		Outputs map[string]map[string]compiledSpecPort `json:"outputs"`
	}

	if err := json.Unmarshal([]byte(compiled), &spec); err != nil {
		return err
	}

	review.Inputs = draftReviewPorts{
		Values:    reviewPortList(spec.Inputs["values"]),
		Artifacts: reviewPortList(spec.Inputs["artifacts"]),
		Secrets:   reviewPortList(spec.Inputs["secrets"]),
	}
	review.Outputs = draftReviewPorts{
		Values:    reviewPortList(spec.Outputs["values"]),
		Artifacts: reviewPortList(spec.Outputs["artifacts"]),
	}

	return nil
}

// reviewPortList converts one compiled-spec section map into a name-sorted
// slice of review ports.
func reviewPortList(section map[string]compiledSpecPort) []draftReviewPort {
	if len(section) == 0 {
		return nil
	}

	ports := make([]draftReviewPort, 0, len(section))

	for name, p := range section {
		port := draftReviewPort{
			Name:        name,
			Type:        p.Schema.Type,
			Required:    p.Required,
			Description: p.Description,
			ContentType: p.ContentType,
		}

		if len(p.Schema.Default) > 0 && string(p.Schema.Default) != "null" {
			var def any
			if err := json.Unmarshal(p.Schema.Default, &def); err == nil {
				port.Default = def
				port.HasDefault = true
			}
		}

		ports = append(ports, port)
	}

	sort.Slice(ports, func(i, j int) bool { return ports[i].Name < ports[j].Name })

	return ports
}

// truthyFlag interprets a graph-node flag that workflow may encode as a bool or a
// non-empty expression string. Anything else reads as unset.
func truthyFlag(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t != ""
	default:
		return false
	}
}

// topoSortReviewNodes orders nodes so every node appears after the nodes it
// needs (Kahn's algorithm in waves, preserving input order within a wave).
// `needs` entries are resolved against node ids first, then short names /
// dotted suffixes (authored specs reference siblings by short name). On a cycle
// or unresolvable tail, the remaining nodes are appended in input order rather
// than dropped.
func topoSortReviewNodes(nodes []draftReviewNode) []draftReviewNode {
	if len(nodes) == 0 {
		return nodes
	}

	ids := make(map[string]bool, len(nodes))
	byShortName := make(map[string]string, len(nodes))

	for _, n := range nodes {
		ids[n.ID] = true

		if idx := strings.LastIndexByte(n.ID, '.'); idx >= 0 {
			byShortName[n.ID[idx+1:]] = n.ID
		}
	}

	resolve := func(need string) string {
		if ids[need] {
			return need
		}

		if full, ok := byShortName[need]; ok {
			return full
		}

		return need
	}

	placed := make(map[string]bool, len(nodes))
	out := make([]draftReviewNode, 0, len(nodes))
	remaining := nodes

	for len(remaining) > 0 {
		var next []draftReviewNode

		progressed := false

		for _, n := range remaining {
			ready := true

			for _, need := range n.Needs {
				if dep := resolve(need); ids[dep] && !placed[dep] {
					ready = false

					break
				}
			}

			if ready {
				out = append(out, n)
				placed[n.ID] = true
				progressed = true
			} else {
				next = append(next, n)
			}
		}

		if !progressed {
			return append(out, next...)
		}

		remaining = next
	}

	return out
}

// renderDraftReviewText prints the review for a human: header, declared IO,
// DAG in dependency order, and the checkpoint instructions (approve → run with
// --approved; changes → session send). Non-contractual; --json is the stable
// shape.
func renderDraftReviewText(review *draftReview, whiteboardID string) {
	header := "Draft " + review.ID

	if review.Revision != nil {
		header += fmt.Sprintf(" — revision %v", review.Revision)
	}

	if review.Status != "" {
		header += ", " + review.Status
	}

	fmt.Println(header)

	if review.SpecNote != "" {
		fmt.Printf("\nnote: %s\n", review.SpecNote)
	}

	printReviewPorts("Inputs", review.Inputs, true)
	printReviewPorts("Outputs", review.Outputs, false)

	fmt.Println("\nDAG (dependency order):")

	if len(review.DAG) == 0 {
		fmt.Println("  (no graph nodes)")
	}

	for _, n := range review.DAG {
		line := "  " + n.ID

		if len(n.Needs) > 0 {
			line += "  needs: " + strings.Join(n.Needs, ", ")
		}

		if n.Kind != "" && n.Kind != "task" {
			line += "  (" + n.Kind + ")"
		}

		if n.QualityGate {
			line += "  [quality-gate]"
		}

		if n.HasRetry {
			line += "  [retry]"
		}

		fmt.Println(line)
	}

	if review.WhiteboardURL != "" {
		fmt.Printf("\nWhiteboard: %s\n", review.WhiteboardURL)
	}

	fmt.Printf(`
Present this to the user (with the whiteboard link when shown above) and ask:
publish and run, select workers/agents, iterate, or stop. On explicit approval
of THIS draft:
  panda workflow draft run %s %s --approved %s
To iterate, send the user's change verbatim via 'session send'.
`, whiteboardID, review.ID, review.ID)
}

// printReviewPorts prints one Inputs/Outputs block. Sections with no declared
// slots are omitted; a fully empty block prints "(none declared)". Secrets are
// only ever printed by name (withSecrets guards the section that has them).
func printReviewPorts(title string, ports draftReviewPorts, withSecrets bool) {
	fmt.Printf("\n%s:\n", title)

	if len(ports.Values) == 0 && len(ports.Artifacts) == 0 && len(ports.Secrets) == 0 {
		fmt.Println("  (none declared)")

		return
	}

	for _, p := range ports.Values {
		fmt.Printf("  values.%s\n", formatReviewPort(p))
	}

	for _, p := range ports.Artifacts {
		line := "  artifacts." + p.Name
		if p.ContentType != "" {
			line += " (" + p.ContentType + ")"
		}

		if p.Description != "" {
			line += "  — " + p.Description
		}

		fmt.Println(line)
	}

	if withSecrets {
		for _, p := range ports.Secrets {
			fmt.Printf("  secrets.%s\n", p.Name)
		}
	}
}

// formatReviewPort renders one values slot as `name (type, default: X,
// required) — description`, omitting absent parts.
func formatReviewPort(p draftReviewPort) string {
	var attrs []string

	if p.Type != "" {
		attrs = append(attrs, p.Type)
	}

	if p.HasDefault {
		attrs = append(attrs, fmt.Sprintf("default: %v", p.Default))
	}

	if p.Required {
		attrs = append(attrs, "required")
	}

	line := p.Name
	if len(attrs) > 0 {
		line += " (" + strings.Join(attrs, ", ") + ")"
	}

	if p.Description != "" {
		line += "  — " + p.Description
	}

	return line
}
