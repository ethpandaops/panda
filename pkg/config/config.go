// Package config provides configuration loading for the MCP server.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ethpandaops/panda/pkg/configpath"
)

const (
	defaultProxyName                    = "primary"
	defaultProxyURL                     = "http://localhost:18081"
	defaultLocalProxyClickHouseName     = "local-kurtosis"
	defaultLocalProxyClickHouseDesc     = "Local Kurtosis devnet logs (OpenTelemetry, autodiscovered). ClickHouse db `otel`, table `otel_logs` (per-service devnet logs; level is in Body, not SeverityText). Filter by EnclaveName (one per devnet) and ServiceName."
	defaultLocalProxyClickHouseHost     = "host.docker.internal"
	defaultLocalProxyClickHousePort     = 18123
	defaultLocalProxyClickHouseDatabase = "otel"
	defaultLocalProxyClickHouseInterval = 10 * time.Second
)

// Config is the main configuration structure.
type Config struct {
	Server  ServerConfig  `yaml:"server"`
	Sandbox SandboxConfig `yaml:"sandbox"`
	// Proxy is the legacy single-proxy form, promoted to Proxies[0] for
	// back-compat. Prefer Proxies (see config.example.yaml).
	Proxy          ProxyConfig          `yaml:"proxy"`
	Proxies        []ProxyConfig        `yaml:"proxies,omitempty"`
	LocalProxy     LocalProxyConfig     `yaml:"local_proxy,omitempty"`
	Storage        StorageConfig        `yaml:"storage"`
	Observability  ObservabilityConfig  `yaml:"observability"`
	ConsensusSpecs ConsensusSpecsConfig `yaml:"consensus_specs,omitempty"`

	path string `yaml:"-"`
}

// ConsensusSpecsConfig configures how consensus-specs are fetched from GitHub.
type ConsensusSpecsConfig struct {
	// Repository is the GitHub owner/repo (e.g. "ethereum/consensus-specs").
	// Defaults to "ethereum/consensus-specs".
	Repository string `yaml:"repository,omitempty"`

	// Ref is the git ref (branch, tag, or SHA) to fetch.
	// When empty, the latest GitHub release tag is used.
	Ref string `yaml:"ref,omitempty"`
}

// StorageConfig holds configuration for local file storage.
type StorageConfig struct {
	// BaseDir is the directory where uploaded files are stored.
	// Defaults to ~/.panda/data/storage.
	BaseDir string `yaml:"base_dir,omitempty"`

	// CacheDir is the directory for the local embedding vector cache.
	// Defaults to a "cache" sibling of BaseDir.
	CacheDir string `yaml:"cache_dir,omitempty"`
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

// SandboxConfig holds sandbox execution configuration.
type SandboxConfig struct {
	Backend        string  `yaml:"backend"`
	Image          string  `yaml:"image"`
	Timeout        int     `yaml:"timeout"`
	MemoryLimit    string  `yaml:"memory_limit"`
	CPULimit       float64 `yaml:"cpu_limit"`
	Network        string  `yaml:"network"`
	HostSharedPath string  `yaml:"host_shared_path,omitempty"`

	// Instance identifies this server's sandbox containers with a custom label.
	// Used to distinguish containers from different server instances (e.g., probe runner vs production).
	// When set, containers are labeled with "io.ethpandaops-panda.instance=<value>".
	Instance string `yaml:"instance,omitempty"`

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
	// Name is the configured proxy identifier used to tag datasource ownership.
	Name string `yaml:"name,omitempty"`

	// URL is the base URL of the proxy server (e.g., http://localhost:18081).
	URL string `yaml:"url"`

	// Auth configures authentication for the proxy.
	// Optional - if not set, the proxy must allow unauthenticated access.
	Auth *ProxyAuthConfig `yaml:"auth,omitempty"`
}

// ProxyAuthConfig configures authentication for the proxy.
type ProxyAuthConfig struct {
	// Mode describes the proxy auth flow. "oauth" is the legacy embedded proxy issuer,
	// "oidc" is an external OpenID Connect issuer such as Dex.
	Mode string `yaml:"mode,omitempty"`

	// IssuerURL is the OAuth issuer URL for proxy authentication.
	IssuerURL string `yaml:"issuer_url"`

	// ClientID is the OAuth client ID for authentication.
	ClientID string `yaml:"client_id"`

	// Resource is the optional OAuth resource indicator to request.
	// Leave empty for standard OIDC providers that do not use RFC 8707 resource parameters.
	Resource string `yaml:"resource,omitempty"`

	// RefreshTokenTTL is the expected lifetime of the refresh token issued by the
	// OIDC provider. When set, the client will proactively refresh at 50% of this
	// duration to keep the refresh token alive via provider rotation.
	RefreshTokenTTL time.Duration `yaml:"refresh_token_ttl,omitempty"`
}

// LocalProxyConfig configures the embedded local proxy used for local
// datasource autodiscovery.
type LocalProxyConfig struct {
	// Enabled controls whether the server starts an in-process local proxy.
	// Defaults to true.
	Enabled *bool `yaml:"enabled,omitempty"`

	// ClickHouse configures local ClickHouse datasources for the embedded proxy.
	ClickHouse []LocalProxyClickHouseConfig `yaml:"clickhouse,omitempty"`
}

// IsEnabled returns whether the embedded local proxy is enabled.
func (c *LocalProxyConfig) IsEnabled() bool {
	if c.Enabled == nil {
		return true
	}

	return *c.Enabled
}

// LocalProxyClickHouseConfig configures a ClickHouse datasource for the embedded proxy.
type LocalProxyClickHouseConfig struct {
	// Name is the datasource name to expose when the probe is live.
	Name string `yaml:"name"`
	// Description is the human-readable datasource description.
	Description string `yaml:"description,omitempty"`
	// Host is the ClickHouse host to proxy.
	Host string `yaml:"host"`
	// Port is the ClickHouse HTTP port to proxy.
	Port int `yaml:"port"`
	// Database is the default database for proxied queries.
	Database string `yaml:"database,omitempty"`
	// Username is the optional datasource username.
	Username string `yaml:"username,omitempty"`
	// Password is the optional datasource password.
	Password string `yaml:"password,omitempty"`
	// Secure switches the proxied ClickHouse target to HTTPS.
	Secure bool `yaml:"secure,omitempty"`
	// Autodiscover probes this datasource and only exposes it while live.
	Autodiscover bool `yaml:"autodiscover,omitempty"`
	// AutodiscoverInterval is how often to check liveness.
	AutodiscoverInterval time.Duration `yaml:"autodiscover_interval,omitempty"`
}

// Load loads configuration from a YAML file with environment variable substitution.
func Load(path string) (*Config, error) {
	resolvedPath, err := configpath.ResolveAppConfigPath(path)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(resolvedPath)
	if err != nil {
		return nil, fmt.Errorf("reading config file %s: %w", resolvedPath, err)
	}

	// Substitute environment variables
	substituted, err := substituteEnvVars(string(data))
	if err != nil {
		return nil, fmt.Errorf("substituting env vars: %w", err)
	}

	var cfg Config
	decoder := yaml.NewDecoder(bytes.NewReader([]byte(substituted)))
	decoder.KnownFields(true)

	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	if err := rejectSingularAndPluralProxies(cfg.Proxy, cfg.Proxies); err != nil {
		return nil, err
	}

	// Apply defaults
	applyDefaults(&cfg)

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validating config: %w", err)
	}

	cfg.path = resolvedPath

	return &cfg, nil
}

// Path returns the resolved path this config was loaded from.
func (c *Config) Path() string {
	return c.path
}

// envVarWithDefaultPattern matches ${VAR_NAME:-default} patterns.
var envVarWithDefaultPattern = regexp.MustCompile(`\$\{([^}:]+)(?::-([^}]*))?\}`)

// substituteEnvVars replaces ${VAR_NAME} and ${VAR_NAME:-default} patterns with environment variable values.
// Lines that are comments (starting with #) are skipped.
// Missing environment variables without defaults are replaced with empty strings (lenient mode).
func substituteEnvVars(content string) (string, error) {
	lines := strings.Split(content, "\n")

	for i, line := range lines {
		// Skip lines that are YAML comments.
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}

		lines[i] = envVarWithDefaultPattern.ReplaceAllStringFunc(line, func(match string) string {
			parts := envVarWithDefaultPattern.FindStringSubmatch(match)
			varName := parts[1]
			defaultVal := ""
			if len(parts) > 2 {
				defaultVal = parts[2]
			}

			value := os.Getenv(varName)
			if value == "" {
				return defaultVal // Use default or empty string
			}

			return value
		})
	}

	return strings.Join(lines, "\n"), nil
}

// applyDefaults sets default values for configuration fields.
func applyDefaults(cfg *Config) {
	if cfg.Server.Host == "" {
		cfg.Server.Host = "0.0.0.0"
	}

	if cfg.Server.Port == 0 {
		cfg.Server.Port = 2480
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

	normalizeProxyConfigs(&cfg.Proxy, &cfg.Proxies, defaultProxyURL)
	applyLocalProxyDefaults(&cfg.LocalProxy)

	// Consensus specs defaults.
	if cfg.ConsensusSpecs.Repository == "" {
		cfg.ConsensusSpecs.Repository = "ethereum/consensus-specs"
	}

	// Storage defaults.
	if cfg.Storage.BaseDir == "" {
		cfg.Storage.BaseDir = pandaDataDir("storage")
	}

	if cfg.Storage.CacheDir == "" {
		cfg.Storage.CacheDir = filepath.Join(filepath.Dir(cfg.Storage.BaseDir), "cache")
	}
}

func normalizeProxyConfigs(primary *ProxyConfig, proxies *[]ProxyConfig, defaultURL string) {
	if len(*proxies) > 0 {
		normalizeProxyList(*proxies)
		*primary = (*proxies)[0]

		return
	}

	if *proxies != nil && !primary.isConfigured() {
		return
	}

	if !primary.isConfigured() && defaultURL == "" {
		return
	}

	if strings.TrimSpace(primary.Name) == "" {
		primary.Name = defaultProxyName
	}

	if strings.TrimSpace(primary.URL) == "" {
		primary.URL = defaultURL
	}

	primary.URL = strings.TrimRight(strings.TrimSpace(primary.URL), "/")
	*proxies = []ProxyConfig{*primary}
}

func rejectSingularAndPluralProxies(primary ProxyConfig, proxies []ProxyConfig) error {
	if primary.isConfigured() && len(proxies) > 0 {
		return fmt.Errorf(`cannot set both "proxy" and "proxies"; use one or the other`)
	}

	return nil
}

func normalizeProxyList(proxies []ProxyConfig) {
	for i := range proxies {
		if strings.TrimSpace(proxies[i].Name) == "" {
			proxies[i].Name = defaultProxyName
			if i > 0 {
				proxies[i].Name = fmt.Sprintf("proxy-%d", i+1)
			}
		}

		proxies[i].URL = strings.TrimRight(strings.TrimSpace(proxies[i].URL), "/")
	}
}

func (c ProxyConfig) isConfigured() bool {
	return strings.TrimSpace(c.Name) != "" ||
		strings.TrimSpace(c.URL) != "" ||
		c.Auth != nil
}

func applyLocalProxyDefaults(cfg *LocalProxyConfig) {
	if cfg.Enabled == nil {
		enabled := true
		cfg.Enabled = &enabled
	}

	if len(cfg.ClickHouse) == 0 {
		cfg.ClickHouse = []LocalProxyClickHouseConfig{defaultLocalProxyClickHouseConfig()}

		return
	}

	for i := range cfg.ClickHouse {
		applyLocalProxyClickHouseDefaults(&cfg.ClickHouse[i])
	}
}

func defaultLocalProxyClickHouseConfig() LocalProxyClickHouseConfig {
	return LocalProxyClickHouseConfig{
		Name:                 defaultLocalProxyClickHouseName,
		Description:          defaultLocalProxyClickHouseDesc,
		Host:                 defaultLocalProxyClickHouseHost,
		Port:                 defaultLocalProxyClickHousePort,
		Database:             defaultLocalProxyClickHouseDatabase,
		Autodiscover:         true,
		AutodiscoverInterval: defaultLocalProxyClickHouseInterval,
	}
}

func applyLocalProxyClickHouseDefaults(cfg *LocalProxyClickHouseConfig) {
	if cfg.Autodiscover && cfg.AutodiscoverInterval == 0 {
		cfg.AutodiscoverInterval = defaultLocalProxyClickHouseInterval
	}

	cfg.Name = strings.TrimSpace(cfg.Name)
	cfg.Description = strings.TrimSpace(cfg.Description)
	cfg.Host = strings.TrimSpace(cfg.Host)
	cfg.Database = strings.TrimSpace(cfg.Database)
}

func pandaDataDir(subdir string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".panda", "data", subdir)
	}

	return filepath.Join(home, ".panda", "data", subdir)
}

// MaxSandboxTimeout is the maximum allowed sandbox timeout in seconds (~3 months).
const MaxSandboxTimeout = 7_776_000

// Validate validates the configuration.
func (c *Config) Validate() error {
	if c.Sandbox.Image == "" {
		return errors.New("sandbox.image is required")
	}

	// Validate sandbox timeout is within bounds.
	if c.Sandbox.Timeout > MaxSandboxTimeout {
		return fmt.Errorf("sandbox.timeout cannot exceed %d seconds", MaxSandboxTimeout)
	}

	seenProxyNames := make(map[string]struct{}, len(c.Proxies))
	for i, proxy := range c.Proxies {
		if strings.TrimSpace(proxy.Name) == "" {
			return fmt.Errorf("proxies[%d].name is required", i)
		}

		if strings.TrimSpace(proxy.URL) == "" {
			return fmt.Errorf("proxies[%d].url is required", i)
		}

		if _, exists := seenProxyNames[proxy.Name]; exists {
			return fmt.Errorf("proxies[%d].name duplicates %q", i, proxy.Name)
		}

		seenProxyNames[proxy.Name] = struct{}{}
	}

	for i, clickhouse := range c.LocalProxy.ClickHouse {
		if strings.TrimSpace(clickhouse.Name) == "" {
			return fmt.Errorf("local_proxy.clickhouse[%d].name is required", i)
		}

		if strings.TrimSpace(clickhouse.Host) == "" {
			return fmt.Errorf("local_proxy.clickhouse[%d].host is required", i)
		}

		if strings.TrimSpace(clickhouse.Database) == "" {
			return fmt.Errorf("local_proxy.clickhouse[%d].database is required", i)
		}

		if clickhouse.AutodiscoverInterval < 0 {
			return fmt.Errorf("local_proxy.clickhouse[%d].autodiscover_interval cannot be negative", i)
		}
	}

	return nil
}
