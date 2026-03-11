// Package types provides shared types used across the MCP server
// and modules to avoid circular dependencies.
package types

// DatasourceInfo describes a configured datasource for the
// datasources:// MCP resources.
type DatasourceInfo struct {
	// Type is the datasource type (e.g. "clickhouse", "prometheus", "loki").
	Type string `json:"type"`
	// Name is the logical name of the datasource.
	Name string `json:"name"`
	// Description is a human-readable description.
	Description string `json:"description,omitempty"`
	// Metadata contains type-specific metadata (e.g. database, url).
	Metadata map[string]string `json:"metadata,omitempty"`
}

// ExampleCategory represents a category of query examples.
type ExampleCategory struct {
	Name        string    `json:"name" yaml:"name"`
	Description string    `json:"description" yaml:"description"`
	Examples    []Example `json:"examples" yaml:"examples"`
}

// Example represents a single query example.
type Example struct {
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description" yaml:"description"`
	Query       string `json:"query" yaml:"query"`
	Cluster     string `json:"cluster" yaml:"cluster"`
}

// ModuleDoc describes a module in the Python library.
type ModuleDoc struct {
	Description string                 `json:"description"`
	Functions   map[string]FunctionDoc `json:"functions"`
}

// FunctionDoc describes a function in the Python library.
type FunctionDoc struct {
	Signature   string            `json:"signature"`
	Description string            `json:"description"`
	Parameters  map[string]string `json:"parameters,omitempty"`
	Returns     string            `json:"returns,omitempty"`
	Example     string            `json:"example,omitempty"`
}
