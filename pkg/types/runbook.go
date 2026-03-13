package types

// Runbook represents a procedural guide for multi-step analysis.
// Runbooks contain markdown content with inline MUST/SHOULD/MAY constraints
// following RFC 2119 conventions.
type Runbook struct {
	Name          string   `yaml:"name" json:"name"`
	Description   string   `yaml:"description" json:"description"`
	Tags          []string `yaml:"tags,omitempty" json:"tags,omitempty"`
	Prerequisites []string `yaml:"prerequisites,omitempty" json:"prerequisites,omitempty"`
	Content       string   `yaml:"-" json:"content"`
	FilePath      string   `yaml:"-" json:"file_path"`
}
