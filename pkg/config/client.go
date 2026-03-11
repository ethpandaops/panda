package config

import (
	"fmt"
	"net"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ethpandaops/panda/pkg/configpath"
)

// ClientConfig is the subset of configuration needed by ep when operating as a server client.
type ClientConfig struct {
	Server ServerConfig `yaml:"server"`

	path string `yaml:"-"`
}

// LoadClient loads client configuration from the standard config locations.
func LoadClient(path string) (*ClientConfig, error) {
	resolvedPath, err := configpath.ResolveAppConfigPath(path)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(resolvedPath)
	if err != nil {
		return nil, fmt.Errorf("reading config file %s: %w", resolvedPath, err)
	}

	substituted, err := substituteEnvVars(string(data))
	if err != nil {
		return nil, fmt.Errorf("substituting env vars: %w", err)
	}

	var cfg ClientConfig
	if err := yaml.Unmarshal([]byte(substituted), &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	cfg.applyDefaults()
	if cfg.ServerURL() == "" {
		return nil, fmt.Errorf(
			"no server URL configured. set server.url or server.base_url in %s",
			resolvedPath,
		)
	}

	cfg.path = resolvedPath

	return &cfg, nil
}

// ServerURL returns the resolved server base URL for client use.
func (c *ClientConfig) ServerURL() string {
	if c == nil {
		return ""
	}

	if c.Server.URL != "" {
		return strings.TrimRight(c.Server.URL, "/")
	}

	if c.Server.BaseURL != "" {
		return strings.TrimRight(c.Server.BaseURL, "/")
	}

	host := strings.TrimSpace(c.Server.Host)
	if host == "" || host == "0.0.0.0" || host == "::" || host == "::0" {
		host = "localhost"
	}

	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}

	port := c.Server.Port
	if port == 0 {
		port = 2480
	}

	return fmt.Sprintf("http://%s", net.JoinHostPort(host, fmt.Sprintf("%d", port)))
}

// Path returns the resolved path this client config was loaded from.
func (c *ClientConfig) Path() string {
	return c.path
}

func (c *ClientConfig) applyDefaults() {
	if c.Server.Port == 0 {
		c.Server.Port = 2480
	}
}
