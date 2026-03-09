package ethnode

// Config holds the ethnode extension configuration.
type Config struct {
	// Enabled controls whether the ethnode extension is active.
	// Defaults to true when configured.
	Enabled *bool `yaml:"enabled,omitempty"`
}

// IsEnabled returns true if the extension is enabled (default: true).
func (c *Config) IsEnabled() bool {
	if c.Enabled == nil {
		return true
	}

	return *c.Enabled
}
