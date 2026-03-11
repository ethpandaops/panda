package clickhouse

import "time"

// Config holds the ClickHouse module configuration.
type Config struct {
	Clusters        []ClusterConfig       `yaml:"clusters"`
	SchemaDiscovery SchemaDiscoveryConfig `yaml:"schema_discovery"`
}

// ClusterConfig holds configuration for a ClickHouse cluster (HTTP/HTTPS only).
type ClusterConfig struct {
	// Name is the logical identifier for this cluster (required).
	Name string `yaml:"name" json:"name"`

	// Description provides context about this cluster for LLM consumption.
	Description string `yaml:"description,omitempty" json:"description,omitempty"`

	// Host is the ClickHouse server address in host:port format (required).
	Host string `yaml:"host" json:"host"`

	// Database is the default database to use (required).
	Database string `yaml:"database" json:"database"`

	// Username is the authentication username (required).
	Username string `yaml:"username" json:"username"`

	// Password is the authentication password (required).
	Password string `yaml:"password" json:"password"`

	// Secure enables TLS for the connection. Defaults to true.
	Secure *bool `yaml:"secure,omitempty" json:"secure,omitempty"`

	// SkipVerify disables TLS certificate verification. Defaults to false.
	SkipVerify bool `yaml:"skip_verify,omitempty" json:"skip_verify,omitempty"`

	// Timeout is the query timeout in seconds. Defaults to 120.
	Timeout int `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}

// SchemaDiscoveryConfig holds configuration for ClickHouse schema discovery.
type SchemaDiscoveryConfig struct {
	// Enabled controls whether schema discovery is active. Defaults to true if datasources are configured.
	Enabled *bool `yaml:"enabled,omitempty"`

	// RefreshInterval is the duration between schema refresh cycles. Defaults to 15 minutes.
	RefreshInterval time.Duration `yaml:"refresh_interval,omitempty"`

	// Datasources lists the ClickHouse datasources to discover schemas from.
	// Each entry references a proxy-exposed datasource by name.
	// If empty, all proxy datasources are used.
	Datasources []SchemaDiscoveryDatasource `yaml:"datasources"`
}

// SchemaDiscoveryDatasource maps a proxy datasource name to a logical cluster name for schema discovery.
type SchemaDiscoveryDatasource struct {
	// Name references a ClickHouse datasource by its proxy name.
	Name string `yaml:"name"`

	// Cluster is the logical cluster name used in schema resources (e.g., "xatu", "xatu-cbt").
	// Defaults to Name when empty.
	Cluster string `yaml:"cluster"`
}

// IsEnabled returns whether schema discovery is enabled.
// Defaults to true if at least one datasource is configured.
func (c *SchemaDiscoveryConfig) IsEnabled() bool {
	if c.Enabled != nil {
		return *c.Enabled
	}

	return len(c.Datasources) > 0
}
