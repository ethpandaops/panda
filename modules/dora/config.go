package dora

type Config struct {
	Enabled *bool `yaml:"enabled,omitempty"`
}

func (c *Config) IsEnabled() bool {
	if c.Enabled == nil {
		return true
	}

	return *c.Enabled
}
