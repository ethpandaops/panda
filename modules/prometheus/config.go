package prometheus

// Config holds the Prometheus module configuration.
// Datasource identity (name, description) comes from the proxy;
// this config only exists for backward compatibility with YAML-based init.
type Config struct {
	Instances []InstanceConfig `yaml:"instances"`
}

// InstanceConfig holds configuration for a Prometheus instance.
// Credential fields are vestigial and ignored at runtime — the proxy
// is the single source of truth for connection details.
type InstanceConfig struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	URL         string `yaml:"url,omitempty" json:"url,omitempty"`
}
