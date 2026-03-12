// Package config provides configuration loading for the MCP server.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ethpandaops/panda/pkg/configpath"
	"github.com/ethpandaops/panda/pkg/configutil"
)

// Config is the main configuration structure.
type Config struct {
	Server         ServerConfig         `yaml:"server"`
	Sandbox        SandboxConfig        `yaml:"sandbox"`
	Proxy          ProxyConfig          `yaml:"proxy"`
	Storage        StorageConfig        `yaml:"storage"`
	Observability  ObservabilityConfig  `yaml:"observability"`
	SemanticSearch SemanticSearchConfig `yaml:"semantic_search"`

	path string `yaml:"-"`
}

// StorageConfig holds configuration for local file storage.
type StorageConfig struct {
	// BaseDir is the directory where uploaded files are stored.
	// Defaults to ~/.panda/data/storage.
	BaseDir string `yaml:"base_dir,omitempty"`
}

// ServerConfig holds server-specific configuration.
type ServerConfig struct {
	Host       string `yaml:"host"`
	Port       int    `yaml:"port"`
	BaseURL    string `yaml:"base_url"`
	SandboxURL string `yaml:"sandbox_url,omitempty"`
	URL        string `yaml:"url,omitempty"`

	// Deprecated: Transport is accepted for backwards compatibility but ignored.
	// The server always runs HTTP with both SSE and streamable-http transports.
	Transport string `yaml:"transport,omitempty"`
}

// SemanticSearchConfig holds configuration for semantic example search.
type SemanticSearchConfig struct {
	// ModelPath is the path to the ONNX embedding model directory.
	// The directory must contain model.onnx and tokenizer.json.
	ModelPath string `yaml:"model_path,omitempty"`
}

// SandboxConfig holds sandbox execution configuration.
type SandboxConfig struct {
	Backend        string  `yaml:"backend"`
	Image          string  `yaml:"image"`
	Timeout        int     `yaml:"timeout"`
	MemoryLimit    string  `yaml:"memory_limit"`
	CPULimit       float64 `yaml:"cpu_limit"`
	Network        string  `yaml:"network"`
	HostSharedPath string  `yaml:"host_shared_path,omitempty"`

	// Session configuration for persistent execution environments.
	Sessions SessionConfig `yaml:"sessions"`

	// Logging configuration for sandbox executions.
	Logging SandboxLoggingConfig `yaml:"logging"`
}

// SandboxLoggingConfig holds logging configuration for sandbox executions.
type SandboxLoggingConfig struct {
	// LogCode logs the full Python code submitted to execute_python.
	// Disabled by default as code may contain sensitive data.
	LogCode bool `yaml:"log_code"`

	// LogOutput logs stdout and stderr from execution.
	// Disabled by default as output may be large or contain sensitive data.
	LogOutput bool `yaml:"log_output"`
}

// SessionConfig holds configuration for persistent sandbox sessions.
type SessionConfig struct {
	// Enabled controls whether session support is available. Defaults to true.
	Enabled *bool `yaml:"enabled,omitempty"`
	// TTL is the duration after which an idle session is destroyed (since last use).
	TTL time.Duration `yaml:"ttl"`
	// MaxDuration is the maximum lifetime of a session regardless of activity.
	MaxDuration time.Duration `yaml:"max_duration"`
	// MaxSessions is the maximum number of concurrent sessions allowed.
	MaxSessions int `yaml:"max_sessions"`
}

// IsEnabled returns whether sessions are enabled (defaults to true).
func (c *SessionConfig) IsEnabled() bool {
	if c.Enabled == nil {
		return true // Default to enabled
	}

	return *c.Enabled
}

// ObservabilityConfig holds observability configuration.
type ObservabilityConfig struct {
	MetricsEnabled bool `yaml:"metrics_enabled"`
	MetricsPort    int  `yaml:"metrics_port"`
}

// ProxyConfig holds proxy connection configuration.
// The MCP server always connects to a proxy server via this config.
type ProxyConfig struct {
	// URL is the base URL of the proxy server (e.g., http://localhost:18081).
	URL string `yaml:"url"`

	// Auth configures authentication for the proxy.
	// Optional - if not set, the proxy must allow unauthenticated access.
	Auth *ProxyAuthConfig `yaml:"auth,omitempty"`
}

// ProxyAuthConfig configures authentication for the proxy.
type ProxyAuthConfig struct {
	// IssuerURL is the OAuth issuer URL for proxy authentication.
	IssuerURL string `yaml:"issuer_url"`

	// ClientID is the OAuth client ID for authentication.
	ClientID string `yaml:"client_id"`
}

// Load loads configuration from a YAML file with environment variable substitution.
func Load(path string) (*Config, error) {
	return load(path, true)
}

func load(path string, validate bool) (*Config, error) {
	resolvedPath, err := configpath.ResolveAppConfigPath(path)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(resolvedPath)
	if err != nil {
		return nil, fmt.Errorf("reading config file %s: %w", resolvedPath, err)
	}

	// Substitute environment variables
	substituted, err := configutil.SubstituteEnvVars(string(data))
	if err != nil {
		return nil, fmt.Errorf("substituting env vars: %w", err)
	}

	var cfg Config
	decoder := yaml.NewDecoder(bytes.NewReader([]byte(substituted)))
	decoder.KnownFields(true)

	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	// Apply defaults
	applyDefaults(&cfg)

	if validate {
		if err := cfg.Validate(); err != nil {
			return nil, fmt.Errorf("validating config: %w", err)
		}
	}

	cfg.path = resolvedPath

	return &cfg, nil
}

// Path returns the resolved path this config was loaded from.
func (c *Config) Path() string {
	return c.path
}

// ServerURL returns the resolved server base URL for client use.
func (c *Config) ServerURL() string {
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

	port := c.Server.Port
	if port == 0 {
		port = 2480
	}

	return fmt.Sprintf("http://%s", net.JoinHostPort(host, fmt.Sprintf("%d", port)))
}

// MaxSandboxTimeout is the maximum allowed sandbox timeout in seconds.
const MaxSandboxTimeout = 600

// Validate validates the configuration.
func (c *Config) Validate() error {
	if c.Sandbox.Image == "" {
		return errors.New("sandbox.image is required")
	}

	// Validate sandbox timeout is within bounds.
	if c.Sandbox.Timeout > MaxSandboxTimeout {
		return fmt.Errorf("sandbox.timeout cannot exceed %d seconds", MaxSandboxTimeout)
	}

	if c.Proxy.URL == "" {
		return errors.New("proxy.url is required")
	}

	return nil
}

// applyDefaults sets default values for configuration fields.
func applyDefaults(cfg *Config) {
	if cfg.Server.Host == "" {
		cfg.Server.Host = "0.0.0.0"
	}

	if cfg.Server.Port == 0 {
		cfg.Server.Port = 2480
	}

	if cfg.Server.Transport == "" {
		// Keep the deprecated field populated for backwards-compatible config
		// handling and tests, even though the runtime no longer branches on it.
		cfg.Server.Transport = "stdio"
	}

	if cfg.Sandbox.Backend == "" {
		cfg.Sandbox.Backend = "docker"
	}

	if cfg.Sandbox.Timeout == 0 {
		cfg.Sandbox.Timeout = 60
	}

	if cfg.Sandbox.MemoryLimit == "" {
		cfg.Sandbox.MemoryLimit = "2g"
	}

	if cfg.Sandbox.CPULimit == 0 {
		cfg.Sandbox.CPULimit = 1.0
	}

	// Session defaults.
	if cfg.Sandbox.Sessions.TTL == 0 {
		cfg.Sandbox.Sessions.TTL = 30 * time.Minute
	}

	if cfg.Sandbox.Sessions.MaxDuration == 0 {
		cfg.Sandbox.Sessions.MaxDuration = 4 * time.Hour
	}

	if cfg.Sandbox.Sessions.MaxSessions == 0 {
		cfg.Sandbox.Sessions.MaxSessions = 10
	}

	if cfg.Observability.MetricsPort == 0 {
		cfg.Observability.MetricsPort = 2490
	}

	// Proxy defaults.
	if cfg.Proxy.URL == "" {
		cfg.Proxy.URL = "http://localhost:18081"
	}

	// Storage defaults.
	if cfg.Storage.BaseDir == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			cfg.Storage.BaseDir = filepath.Join(home, ".panda", "data", "storage")
		} else {
			cfg.Storage.BaseDir = filepath.Join(".", ".panda", "data", "storage")
		}
	}

	// Semantic search defaults — model path is resolved at runtime by searchruntime.
	// Leave empty to use the default search paths.
}
