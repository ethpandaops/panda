package proxy

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	simpleauth "github.com/ethpandaops/mcp/pkg/auth"
	"github.com/ethpandaops/mcp/pkg/configpath"
	"github.com/ethpandaops/mcp/pkg/proxy/handlers"
)

// ServerConfig is the configuration for the proxy server.
// This is the single configuration schema used for both local and K8s deployments.
type ServerConfig struct {
	// Server holds HTTP server configuration.
	Server HTTPServerConfig `yaml:"server"`

	// Auth holds authentication configuration.
	Auth AuthConfig `yaml:"auth"`

	// ClickHouse holds ClickHouse cluster configurations.
	ClickHouse []ClickHouseClusterConfig `yaml:"clickhouse,omitempty"`

	// Prometheus holds Prometheus instance configurations.
	Prometheus []PrometheusInstanceConfig `yaml:"prometheus,omitempty"`

	// Loki holds Loki instance configurations.
	Loki []LokiInstanceConfig `yaml:"loki,omitempty"`

	// S3 holds S3 storage configuration.
	S3 *S3Config `yaml:"s3,omitempty"`

	// EthNode holds Ethereum node API access configuration.
	EthNode *EthNodeInstanceConfig `yaml:"ethnode,omitempty"`

	// RateLimiting holds rate limiting configuration.
	RateLimiting RateLimitConfig `yaml:"rate_limiting"`

	// Audit holds audit logging configuration.
	Audit AuditConfig `yaml:"audit"`
}

// HTTPServerConfig holds HTTP server configuration.
type HTTPServerConfig struct {
	// ListenAddr is the address to listen on (default: ":18081").
	ListenAddr string `yaml:"listen_addr,omitempty"`

	// ReadTimeout is the maximum duration for reading the entire request.
	ReadTimeout time.Duration `yaml:"read_timeout,omitempty"`

	// WriteTimeout is the maximum duration before timing out writes of the response.
	WriteTimeout time.Duration `yaml:"write_timeout,omitempty"`

	// IdleTimeout is the maximum amount of time to wait for the next request.
	IdleTimeout time.Duration `yaml:"idle_timeout,omitempty"`
}

// AuthConfig holds authentication configuration for the proxy.
type AuthConfig struct {
	// Mode is the authentication mode.
	Mode AuthMode `yaml:"mode"`

	// GitHub configures the GitHub OAuth app used for user authentication.
	GitHub *simpleauth.GitHubConfig `yaml:"github,omitempty"`

	// AllowedOrgs restricts access to members of these GitHub orgs.
	AllowedOrgs []string `yaml:"allowed_orgs,omitempty"`

	// Tokens configures proxy-issued bearer tokens.
	Tokens simpleauth.TokensConfig `yaml:"tokens"`

	// AccessTokenTTL is the lifetime of proxy-issued access tokens.
	AccessTokenTTL time.Duration `yaml:"access_token_ttl,omitempty"`
}

// ClickHouseClusterConfig holds ClickHouse cluster configuration.
type ClickHouseClusterConfig struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description,omitempty"`
	Host        string `yaml:"host"`
	Port        int    `yaml:"port"`
	Database    string `yaml:"database,omitempty"`
	Username    string `yaml:"username"`
	Password    string `yaml:"password"`
	Secure      bool   `yaml:"secure"`
	SkipVerify  bool   `yaml:"skip_verify,omitempty"`
	Timeout     int    `yaml:"timeout,omitempty"`
}

// PrometheusInstanceConfig holds Prometheus instance configuration.
type PrometheusInstanceConfig struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description,omitempty"`
	URL         string `yaml:"url"`
	Username    string `yaml:"username,omitempty"`
	Password    string `yaml:"password,omitempty"`
}

// LokiInstanceConfig holds Loki instance configuration.
type LokiInstanceConfig struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description,omitempty"`
	URL         string `yaml:"url"`
	Username    string `yaml:"username,omitempty"`
	Password    string `yaml:"password,omitempty"`
}

// EthNodeInstanceConfig holds Ethereum node API access configuration.
// A single credential pair is used for all beacon and execution node endpoints.
type EthNodeInstanceConfig struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

// S3Config holds S3 storage configuration.
type S3Config struct {
	Endpoint        string `yaml:"endpoint"`
	AccessKey       string `yaml:"access_key"`
	SecretKey       string `yaml:"secret_key"`
	Bucket          string `yaml:"bucket"`
	Region          string `yaml:"region,omitempty"`
	PublicURLPrefix string `yaml:"public_url_prefix,omitempty"`
}

// RateLimitConfig holds rate limiting configuration.
type RateLimitConfig struct {
	// Enabled controls whether rate limiting is active.
	Enabled bool `yaml:"enabled"`

	// RequestsPerMinute is the maximum requests per minute per user.
	RequestsPerMinute int `yaml:"requests_per_minute,omitempty"`

	// BurstSize is the maximum burst size.
	BurstSize int `yaml:"burst_size,omitempty"`
}

// AuditConfig holds audit logging configuration.
type AuditConfig struct {
	// Enabled controls whether audit logging is active.
	Enabled bool `yaml:"enabled"`

	// LogQueries controls whether to log query content.
	LogQueries bool `yaml:"log_queries,omitempty"`

	// MaxQueryLength is the maximum length of query to log.
	MaxQueryLength int `yaml:"max_query_length,omitempty"`
}

// ApplyDefaults sets default values for the server config.
func (c *ServerConfig) ApplyDefaults() {
	// Server defaults.
	if c.Server.ListenAddr == "" {
		c.Server.ListenAddr = ":18081"
	}

	if c.Server.ReadTimeout == 0 {
		c.Server.ReadTimeout = 30 * time.Second
	}

	if c.Server.WriteTimeout == 0 {
		c.Server.WriteTimeout = 5 * time.Minute
	}

	if c.Server.IdleTimeout == 0 {
		c.Server.IdleTimeout = 60 * time.Second
	}

	// Auth defaults.
	// Default to no auth for local development. Hosted deployments should explicitly set oauth mode.
	if c.Auth.Mode == "" {
		c.Auth.Mode = AuthModeNone
	}

	if c.Auth.AccessTokenTTL == 0 {
		c.Auth.AccessTokenTTL = 1 * time.Hour
	}

	// Rate limiting defaults.
	if c.RateLimiting.RequestsPerMinute == 0 {
		c.RateLimiting.RequestsPerMinute = 60
	}

	if c.RateLimiting.BurstSize == 0 {
		c.RateLimiting.BurstSize = 10
	}

	// Audit defaults.
	if c.Audit.MaxQueryLength == 0 {
		c.Audit.MaxQueryLength = 500
	}

	// ClickHouse defaults.
	for i := range c.ClickHouse {
		if c.ClickHouse[i].Port == 0 {
			if c.ClickHouse[i].Secure {
				c.ClickHouse[i].Port = 8443
			} else {
				c.ClickHouse[i].Port = 8123
			}
		}
	}
}

// Validate validates the server config.
func (c *ServerConfig) Validate() error {
	if c.Auth.Mode == AuthModeOAuth {
		if c.Auth.GitHub == nil {
			return fmt.Errorf("auth.github is required when auth.mode is 'oauth'")
		}

		if c.Auth.GitHub.ClientID == "" {
			return fmt.Errorf("auth.github.client_id is required")
		}

		if c.Auth.GitHub.ClientSecret == "" {
			return fmt.Errorf("auth.github.client_secret is required")
		}

		if c.Auth.Tokens.SecretKey == "" {
			return fmt.Errorf("auth.tokens.secret_key is required")
		}
	}

	// Validate at least one datasource is configured.
	if len(c.ClickHouse) == 0 && len(c.Prometheus) == 0 && len(c.Loki) == 0 && c.EthNode == nil {
		return fmt.Errorf("at least one datasource (clickhouse, prometheus, loki, or ethnode) must be configured")
	}

	// Validate ClickHouse configs.
	for i, ch := range c.ClickHouse {
		if ch.Name == "" {
			return fmt.Errorf("clickhouse[%d].name is required", i)
		}

		if ch.Host == "" {
			return fmt.Errorf("clickhouse[%d].host is required", i)
		}
	}

	// Validate Prometheus configs.
	for i, prom := range c.Prometheus {
		if prom.Name == "" {
			return fmt.Errorf("prometheus[%d].name is required", i)
		}

		if prom.URL == "" {
			return fmt.Errorf("prometheus[%d].url is required", i)
		}
	}

	// Validate Loki configs.
	for i, loki := range c.Loki {
		if loki.Name == "" {
			return fmt.Errorf("loki[%d].name is required", i)
		}

		if loki.URL == "" {
			return fmt.Errorf("loki[%d].url is required", i)
		}
	}

	return nil
}

// ToHandlerConfigs converts the server config to handler configs.
func (c *ServerConfig) ToHandlerConfigs() ([]handlers.ClickHouseConfig, []handlers.PrometheusConfig, []handlers.LokiConfig, *handlers.S3Config, *handlers.EthNodeConfig) {
	// Convert ClickHouse configs.
	chConfigs := make([]handlers.ClickHouseConfig, len(c.ClickHouse))
	for i, ch := range c.ClickHouse {
		chConfigs[i] = handlers.ClickHouseConfig{
			Name:        ch.Name,
			Description: ch.Description,
			Host:        ch.Host,
			Port:        ch.Port,
			Database:    ch.Database,
			Username:    ch.Username,
			Password:    ch.Password,
			Secure:      ch.Secure,
			SkipVerify:  ch.SkipVerify,
			Timeout:     ch.Timeout,
		}
	}

	// Convert Prometheus configs.
	promConfigs := make([]handlers.PrometheusConfig, len(c.Prometheus))
	for i, prom := range c.Prometheus {
		promConfigs[i] = handlers.PrometheusConfig{
			Name:        prom.Name,
			Description: prom.Description,
			URL:         prom.URL,
			Username:    prom.Username,
			Password:    prom.Password,
		}
	}

	// Convert Loki configs.
	lokiConfigs := make([]handlers.LokiConfig, len(c.Loki))
	for i, loki := range c.Loki {
		lokiConfigs[i] = handlers.LokiConfig{
			Name:        loki.Name,
			Description: loki.Description,
			URL:         loki.URL,
			Username:    loki.Username,
			Password:    loki.Password,
		}
	}

	// Convert S3 config.
	var s3Config *handlers.S3Config
	if c.S3 != nil && c.S3.Endpoint != "" {
		s3Config = &handlers.S3Config{
			Endpoint:        c.S3.Endpoint,
			AccessKey:       c.S3.AccessKey,
			SecretKey:       c.S3.SecretKey,
			Bucket:          c.S3.Bucket,
			Region:          c.S3.Region,
			PublicURLPrefix: c.S3.PublicURLPrefix,
		}
	}

	// Convert EthNode config.
	var ethNodeConfig *handlers.EthNodeConfig
	if c.EthNode != nil && c.EthNode.Username != "" {
		ethNodeConfig = &handlers.EthNodeConfig{
			Username: c.EthNode.Username,
			Password: c.EthNode.Password,
		}
	}

	return chConfigs, promConfigs, lokiConfigs, s3Config, ethNodeConfig
}

// envVarWithDefaultPattern matches ${VAR_NAME:-default} patterns.
var envVarWithDefaultPattern = regexp.MustCompile(`\$\{([^}:]+)(?::-([^}]*))?\}`)

// LoadServerConfig loads a proxy server config from a YAML file.
func LoadServerConfig(path string) (*ServerConfig, error) {
	resolvedPath, err := configpath.ResolveProxyConfigPath(path, "")
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(resolvedPath)
	if err != nil {
		return nil, fmt.Errorf("reading config file %s: %w", resolvedPath, err)
	}

	// Substitute environment variables.
	substituted, err := substituteEnvVars(string(data))
	if err != nil {
		return nil, fmt.Errorf("substituting env vars: %w", err)
	}

	var cfg ServerConfig
	if err := yaml.Unmarshal([]byte(substituted), &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	cfg.ApplyDefaults()

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validating config: %w", err)
	}

	return &cfg, nil
}

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
