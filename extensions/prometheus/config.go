package prometheus

import "fmt"

// Config holds the Prometheus extension configuration.
type Config struct {
	Instances []InstanceConfig `yaml:"instances"`
}

// InstanceConfig holds configuration for a Prometheus instance.
type InstanceConfig struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	URL         string `yaml:"url" json:"url"`
	Username    string `yaml:"username,omitempty" json:"username,omitempty"`
	Password    string `yaml:"password,omitempty" json:"password,omitempty"`
	SkipVerify  bool   `yaml:"skip_verify,omitempty" json:"skip_verify,omitempty"`
	Timeout     int    `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}

// Validate validates the configuration.
func (c *Config) Validate() error {
	names := make(map[string]struct{}, len(c.Instances))
	for i, inst := range c.Instances {
		if inst.Name == "" {
			return fmt.Errorf("instances[%d].name is required", i)
		}
		if _, exists := names[inst.Name]; exists {
			return fmt.Errorf("instances[%d].name %q is duplicated", i, inst.Name)
		}
		names[inst.Name] = struct{}{}
		if inst.URL == "" {
			return fmt.Errorf("instances[%d].url is required", i)
		}
	}
	return nil
}

// ApplyDefaults applies default values to the configuration.
func (c *Config) ApplyDefaults() {
	for i := range c.Instances {
		if c.Instances[i].Timeout == 0 {
			c.Instances[i].Timeout = 60
		}
	}
}
