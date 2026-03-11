package ethnode

// Config holds the ethnode module configuration.
type Config struct {
	// Enabled controls whether the ethnode module is active.
	// Defaults to true when configured.
	Enabled *bool `yaml:"enabled,omitempty"`
}

// IsEnabled returns true if the module is enabled (default: true).
func (c *Config) IsEnabled() bool {
	if c.Enabled == nil {
		return true
	}

	return *c.Enabled
}
