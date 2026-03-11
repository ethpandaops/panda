package clickhouse

import "time"

// Config holds the ClickHouse module configuration.
type Config struct {
	SchemaDiscovery SchemaDiscoveryConfig `yaml:"schema_discovery"`
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
// Defaults to true; set enabled=false to disable explicitly.
func (c *SchemaDiscoveryConfig) IsEnabled() bool {
	if c.Enabled != nil {
		return *c.Enabled
	}

	return true
}
