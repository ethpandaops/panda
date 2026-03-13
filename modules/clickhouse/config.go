package clickhouse

import "time"

type Config struct {
	SchemaDiscovery SchemaDiscoveryConfig `yaml:"schema_discovery"`
}

type SchemaDiscoveryConfig struct {
	Enabled         *bool                       `yaml:"enabled,omitempty"`
	RefreshInterval time.Duration               `yaml:"refresh_interval,omitempty"`
	Datasources     []SchemaDiscoveryDatasource `yaml:"datasources"`
}

type SchemaDiscoveryDatasource struct {
	Name    string `yaml:"name"`
	Cluster string `yaml:"cluster"`
}

func (c *SchemaDiscoveryConfig) IsEnabled() bool {
	if c.Enabled != nil {
		return *c.Enabled
	}

	return true
}
