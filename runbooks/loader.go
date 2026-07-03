// Package runbooks provides embedded runbook documents for procedural guidance.
package runbooks

import (
	"bytes"
	"embed"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ethpandaops/panda/pkg/types"
)

//go:embed *.md
var runbookFiles embed.FS

// nonRunbookDocs are markdown files in this directory that are repository
// documentation (authoring guides, agent instructions), not runbooks. They are
// embedded by the wildcard but must not be parsed as runbooks.
var nonRunbookDocs = map[string]bool{
	"AGENTS.md": true,
	"CLAUDE.md": true,
	"README.md": true,
}

// Load reads all embedded markdown files and parses them into Runbook objects.
// Each file must have YAML frontmatter delimited by "---" markers.
func Load() ([]types.Runbook, error) {
	entries, err := runbookFiles.ReadDir(".")
	if err != nil {
		return nil, fmt.Errorf("reading runbook directory: %w", err)
	}

	runbooks := make([]types.Runbook, 0, len(entries))

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		if nonRunbookDocs[entry.Name()] {
			continue
		}

		data, err := runbookFiles.ReadFile(entry.Name())
		if err != nil {
			return nil, fmt.Errorf("reading runbook %s: %w", entry.Name(), err)
		}

		rb, err := parseRunbook(data, entry.Name())
		if err != nil {
			return nil, fmt.Errorf("parsing runbook %s: %w", entry.Name(), err)
		}

		runbooks = append(runbooks, rb)
	}

	return runbooks, nil
}

// parseRunbook extracts YAML frontmatter and markdown body from file content.
func parseRunbook(data []byte, filename string) (types.Runbook, error) {
	frontmatter, body, err := splitFrontmatter(data)
	if err != nil {
		return types.Runbook{}, err
	}

	var rb types.Runbook
	if err := yaml.Unmarshal(frontmatter, &rb); err != nil {
		return types.Runbook{}, fmt.Errorf("unmarshaling frontmatter: %w", err)
	}

	rb.Content = strings.TrimSpace(string(body))
	rb.FilePath = filename

	if rb.Name == "" {
		return types.Runbook{}, fmt.Errorf("runbook must have a name in frontmatter")
	}

	if rb.Description == "" {
		return types.Runbook{}, fmt.Errorf("runbook must have a description in frontmatter")
	}

	if len(rb.Triggers) == 0 {
		return types.Runbook{}, fmt.Errorf("runbook must have triggers in frontmatter (example caller queries)")
	}

	return rb, nil
}

// splitFrontmatter separates YAML frontmatter from markdown body.
// Frontmatter must be delimited by "---" at the start and end.
func splitFrontmatter(data []byte) (frontmatter, body []byte, err error) {
	const delimiter = "---"

	data = bytes.TrimSpace(data)
	if !bytes.HasPrefix(data, []byte(delimiter)) {
		return nil, nil, fmt.Errorf("file must start with YAML frontmatter delimiter '---'")
	}

	// Skip first delimiter
	data = data[len(delimiter):]

	// Find end delimiter
	before, after, found := bytes.Cut(data, []byte("\n"+delimiter))
	if !found {
		return nil, nil, fmt.Errorf("missing closing frontmatter delimiter '---'")
	}

	return bytes.TrimSpace(before), bytes.TrimSpace(after), nil
}
