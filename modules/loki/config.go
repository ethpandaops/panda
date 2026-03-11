package loki

// Config holds the Loki module configuration.
type Config struct {
	Instances []InstanceConfig `yaml:"instances"`
}

// InstanceConfig holds configuration for a Loki instance.
type InstanceConfig struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	URL         string `yaml:"url,omitempty" json:"url,omitempty"`
}
