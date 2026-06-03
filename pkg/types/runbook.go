package types

// Runbook represents a procedural guide for multi-step analysis.
// Runbooks contain markdown content with inline MUST/SHOULD/MAY constraints
// following RFC 2119 conventions.
type Runbook struct {
	// Name is the title of the runbook (imperative, e.g., "Investigate Finality Delay").
	Name string `yaml:"name" json:"name"`
	// Description is a 1-2 sentence summary for semantic search matching.
	Description string `yaml:"description" json:"description"`
	// Tags are keywords for search (e.g., "finality", "consensus", "attestations").
	Tags []string `yaml:"tags,omitempty" json:"tags,omitempty"`
	// Prerequisites lists datasources needed (e.g., "clickhouse-raw", "prometheus", "dora").
	Prerequisites []string `yaml:"prerequisites,omitempty" json:"prerequisites,omitempty"`
	// Content is the markdown body (not from frontmatter).
	Content string `yaml:"-" json:"content"`
	// FilePath is the source file for debugging.
	FilePath string `yaml:"-" json:"file_path"`
}
