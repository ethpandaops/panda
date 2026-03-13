package config

import (
	"fmt"
)

// ClientConfig is the client-facing view of the shared application config.
type ClientConfig = Config

// LoadClient loads client configuration from the standard config locations.
func LoadClient(path string) (*ClientConfig, error) {
	cfg, err := load(path, false)
	if err != nil {
		return nil, err
	}

	if cfg.ServerURL() == "" {
		return nil, fmt.Errorf(
			"no server URL configured. set server.url or server.base_url in %s",
			cfg.Path(),
		)
	}

	return cfg, nil
}
