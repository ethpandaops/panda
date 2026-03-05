package ethnode

// Config holds the ethnode plugin configuration.
type Config struct {
	// Enabled controls whether the ethnode plugin is active.
	// Defaults to true when configured.
	Enabled *bool `yaml:"enabled,omitempty"`
}

// IsEnabled returns true if the plugin is enabled (default: true).
func (c *Config) IsEnabled() bool {
	if c.Enabled == nil {
		return true
	}

	return *c.Enabled
}
