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
	// ProxyName is the configured proxy that owns this datasource.
	ProxyName string `json:"proxy_name,omitempty"`
	// Description is a human-readable description.
	Description string `json:"description,omitempty"`
	// Metadata contains type-specific metadata (e.g. database, url).
	Metadata map[string]string `json:"metadata,omitempty"`
	// Contents declares the datasets stored in this datasource. It is authored
	// by the proxy owner and passed through opaquely: the proxy never interprets
	// Params or Notes. A datasource may hold multiple datasets (and the same
	// dataset may appear more than once, e.g. in different databases).
	Contents []DatasetBinding `json:"contents,omitempty"`
}

// DatasetBinding declares that a named dataset lives in a datasource, with
// opaque placement params and operator notes. The dataset name matches a
// knowledge pack shipped in the release; Params are interpreted by that pack
// (e.g. {"database": "default"}); Notes says what distinguishes this copy from
// the dataset's other copies (e.g. "operator infrastructure logs" vs "devnet
// node logs") — universal query knowledge belongs in the dataset pack, and
// cluster-wide behavior in the datasource description.
type DatasetBinding struct {
	Dataset string            `json:"dataset" yaml:"dataset"`
	Params  map[string]string `json:"params,omitempty" yaml:"params,omitempty"`
	Notes   string            `json:"notes,omitempty" yaml:"notes,omitempty"`
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
	// Target identifies the datasource the example runs against: a ClickHouse
	// cluster name, or a Prometheus/Loki datasource name.
	Target string `json:"target" yaml:"target"`
	// Dataset is the knowledge pack the example ships in (e.g. xatu-raw). It is
	// stamped by the datasets module at load time, never authored in YAML.
	Dataset string `json:"dataset,omitempty" yaml:"-"`
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
