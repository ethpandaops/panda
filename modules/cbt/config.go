package cbt

// Config holds the CBT module configuration.
// CBT is enabled by default since it's a public service and
// requires no credentials.
type Config struct {
	// Enabled controls whether the CBT module is active.
	// Defaults to true.
	Enabled *bool `yaml:"enabled,omitempty"`
}

// IsEnabled returns true if the module is enabled (default: true).
func (c *Config) IsEnabled() bool {
	if c.Enabled == nil {
		return true
	}

	return *c.Enabled
}
