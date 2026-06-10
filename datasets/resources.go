package datasets

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/module"
	"github.com/ethpandaops/panda/pkg/types"
)

// datasetURIPattern matches datasets://{name} URIs.
var datasetURIPattern = regexp.MustCompile(`^datasets://(.+)$`)

// datasetSummary is one entry in the datasets://list response.
type datasetSummary struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Active reports whether this deployment exposes the dataset (declared via
	// proxy contains, or no declarations exist at all).
	Active     bool        `json:"active"`
	Placements []placement `json:"placements,omitempty"`
}

// RegisterResources registers the datasets://list and datasets://{name}
// resources.
func (m *Module) RegisterResources(log logrus.FieldLogger, reg module.ResourceRegistry) error {
	// The discovery-refresh goroutine reads the logger concurrently (warn
	// paths in InitFromDiscovery / logDropped), so swap it under the lock.
	m.mu.Lock()
	m.log = log.WithField("module", "datasets")
	m.mu.Unlock()

	reg.RegisterStatic(types.StaticResource{
		Resource: mcp.NewResource(
			"datasets://list",
			"Datasets",
			mcp.WithResourceDescription("Datasets known to this release: name, description, whether this deployment holds them, and where"),
			mcp.WithMIMEType("application/json"),
			mcp.WithAnnotations([]mcp.Role{mcp.RoleAssistant}, 0.7, ""),
		),
		Handler: m.handleDatasetsList,
	})

	reg.RegisterTemplate(types.TemplateResource{
		Template: mcp.NewResourceTemplate(
			"datasets://{name}",
			"Dataset Guide",
			mcp.WithTemplateDescription("Full guide for one dataset: placement in this deployment, query syntax rules, and example categories. Read before querying the dataset."),
			mcp.WithTemplateMIMEType("text/markdown"),
			mcp.WithTemplateAnnotations([]mcp.Role{mcp.RoleAssistant}, 0.8, ""),
		),
		Pattern: datasetURIPattern,
		Handler: m.handleDatasetDetail,
	})

	return nil
}

func (m *Module) handleDatasetsList(_ context.Context, _ string) (string, error) {
	exposed := make(map[string]bool, len(m.packs))
	for _, p := range m.activePacks() {
		exposed[p.name] = true
	}

	summaries := make([]datasetSummary, 0, len(m.packs))
	for _, p := range m.packs {
		summaries = append(summaries, datasetSummary{
			Name:        p.name,
			Description: p.description,
			Active:      exposed[p.name],
			Placements:  m.packPlacements(p.name),
		})
	}

	data, err := json.MarshalIndent(map[string]any{
		"datasets": summaries,
		"usage":    "Read datasets://{name} for a dataset's query guide. Placement params/notes also appear in datasources://clickhouse.",
	}, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshaling datasets list: %w", err)
	}

	return string(data), nil
}

func (m *Module) handleDatasetDetail(ctx context.Context, uri string) (string, error) {
	matches := datasetURIPattern.FindStringSubmatch(uri)
	if len(matches) != 2 {
		return "", fmt.Errorf("invalid URI format: %s", uri)
	}

	name := matches[1]
	if name == "list" {
		return m.handleDatasetsList(context.Background(), uri)
	}

	for _, p := range m.packs {
		if p.name != name {
			continue
		}

		return m.renderDatasetGuide(p, types.GetClientContext(ctx)), nil
	}

	names := make([]string, 0, len(m.packs))
	for _, p := range m.packs {
		names = append(names, p.name)
	}

	return "", fmt.Errorf("unknown dataset %q. Known datasets: %s", name, strings.Join(names, ", "))
}

// renderDatasetGuide assembles the full per-dataset guide: identity, where the
// dataset lives in this deployment, the pack's guidance, and its example
// categories.
func (m *Module) renderDatasetGuide(p pack, clientCtx types.ClientContext) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# %s — %s\n\n", p.name, p.description)

	b.WriteString("## Placement in this deployment\n\n")

	placements := m.packPlacements(p.name)
	if len(placements) == 0 {
		b.WriteString("No datasource in this deployment declares this dataset (or the proxy predates `contains` declarations). Check `datasources://clickhouse`.\n")
	}

	for _, pl := range placements {
		fmt.Fprintf(&b, "- datasource `%s`", pl.Datasource)

		if len(pl.Params) > 0 {
			keys := make([]string, 0, len(pl.Params))
			for k := range pl.Params {
				keys = append(keys, k)
			}

			sort.Strings(keys)

			pairs := make([]string, 0, len(keys))
			for _, k := range keys {
				pairs = append(pairs, fmt.Sprintf("%s=%s", k, pl.Params[k]))
			}

			fmt.Fprintf(&b, " — %s", strings.Join(pairs, ", "))
		}

		if pl.Notes != "" {
			fmt.Fprintf(&b, "\n  - note: %s", pl.Notes)
		}

		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(p.gettingStarted)
	b.WriteString("\n\n## Examples\n\n")

	keys := make([]string, 0, len(p.examples))
	for key := range p.examples {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	for _, key := range keys {
		fmt.Fprintf(&b, "- %s (%d examples)\n", key, len(p.examples[key].Examples))
	}

	if clientCtx == types.ClientContextCLI {
		fmt.Fprintf(&b, "\nRetrieve them with: panda search examples --dataset %s \"<topic>\".\n", p.name)
	} else {
		fmt.Fprintf(&b, "\nRetrieve them with the search tool: search(type=\"examples\", dataset=%q, query=\"...\").\n", p.name)
	}

	return b.String()
}
