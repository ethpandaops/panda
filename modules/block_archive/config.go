package blockarchive

// DefaultURL is the production block-archiver public endpoint.
const DefaultURL = "https://block-archiver.analytics.production.platform.ethpandaops.io"

// Config holds the Block Archive module configuration.
// Block Archive is enabled by default since it's a public service and
// requires no credentials.
type Config struct {
	// Enabled controls whether the Block Archive module is active.
	// Defaults to true.
	Enabled *bool `yaml:"enabled,omitempty"`

	// URL is the base URL of the block-archiver service. Defaults to the
	// production endpoint.
	URL string `yaml:"url,omitempty"`
}

// IsEnabled returns true if the module is enabled (default: true).
func (c *Config) IsEnabled() bool {
	if c.Enabled == nil {
		return true
	}

	return *c.Enabled
}

// BaseURL returns the configured URL or the default.
func (c *Config) BaseURL() string {
	if c.URL == "" {
		return DefaultURL
	}

	return c.URL
}
