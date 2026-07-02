package types

// Runbook represents a procedural guide for multi-step analysis, served to
// agents via semantic search and direct ref reads. The authoring standard
// lives in runbooks/AGENTS.md.
type Runbook struct {
	// Name is the title of the runbook (imperative, e.g., "Investigate Finality Delay").
	Name string `yaml:"name" json:"name"`
	// Description is a 1-2 sentence summary for semantic search matching.
	Description string `yaml:"description" json:"description"`
	// Tags are keywords for search (e.g., "finality", "consensus", "attestations").
	Tags []string `yaml:"tags,omitempty" json:"tags,omitempty"`
	// Triggers are example caller queries this runbook should match in semantic
	// search (e.g., "why is finality stalled"). They are embedded alongside the
	// name and description to widen the retrieval surface.
	Triggers []string `yaml:"triggers,omitempty" json:"triggers,omitempty"`
	// Prerequisites lists datasources needed (e.g., "clickhouse-raw", "prometheus", "dora").
	Prerequisites []string `yaml:"prerequisites,omitempty" json:"prerequisites,omitempty"`
	// Content is the markdown body (not from frontmatter).
	Content string `yaml:"-" json:"content"`
	// FilePath is the source file for debugging.
	FilePath string `yaml:"-" json:"file_path"`
}
