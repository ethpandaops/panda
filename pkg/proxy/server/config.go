package proxyserver

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"

	simpleauth "github.com/ethpandaops/panda/pkg/auth"
	"github.com/ethpandaops/panda/pkg/configpath"
	"github.com/ethpandaops/panda/pkg/configutil"
)

// ServerConfig is the single configuration schema for the standalone proxy server.
type ServerConfig struct {
	Server       HTTPServerConfig             `yaml:"server"`
	Auth         AuthConfig                   `yaml:"auth"`
	ClickHouse   []ClickHouseDatasourceConfig `yaml:"clickhouse,omitempty"`
	Prometheus   []PrometheusInstanceConfig   `yaml:"prometheus,omitempty"`
	Loki         []LokiInstanceConfig         `yaml:"loki,omitempty"`
	S3           *S3Config                    `yaml:"s3,omitempty"`
	EthNode      *EthNodeInstanceConfig       `yaml:"ethnode,omitempty"`
	RateLimiting RateLimitConfig              `yaml:"rate_limiting"`
	Audit        AuditConfig                  `yaml:"audit"`
	Metrics      MetricsConfig                `yaml:"metrics"`
}

// HTTPServerConfig controls the proxy listener and HTTP timeouts.
type HTTPServerConfig struct {
	ListenAddr   string        `yaml:"listen_addr,omitempty"`
	ReadTimeout  time.Duration `yaml:"read_timeout,omitempty"`
	WriteTimeout time.Duration `yaml:"write_timeout,omitempty"`
	IdleTimeout  time.Duration `yaml:"idle_timeout,omitempty"`
}

// AuthConfig holds authentication configuration for the proxy.
type AuthConfig struct {
	Mode           AuthMode                      `yaml:"mode"`
	GitHub         *simpleauth.GitHubConfig      `yaml:"github,omitempty"`
	AllowedOrgs    []string                      `yaml:"allowed_orgs,omitempty"`
	Tokens         simpleauth.TokensConfig       `yaml:"tokens"`
	AccessTokenTTL time.Duration                 `yaml:"access_token_ttl,omitempty"`
	SuccessPage    *simpleauth.SuccessPageConfig `yaml:"success_page,omitempty"`
}

// ClickHouseDatasourceConfig holds ClickHouse datasource configuration.
type ClickHouseDatasourceConfig struct {
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

// ClickHouseClusterConfig is kept as an alias while downstream code migrates
// to the datasource terminology.
type ClickHouseClusterConfig = ClickHouseDatasourceConfig

type PrometheusInstanceConfig struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description,omitempty"`
	URL         string `yaml:"url"`
	Username    string `yaml:"username,omitempty"`
	Password    string `yaml:"password,omitempty"`
}

type LokiInstanceConfig struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description,omitempty"`
	URL         string `yaml:"url"`
	Username    string `yaml:"username,omitempty"`
	Password    string `yaml:"password,omitempty"`
}

// EthNodeInstanceConfig uses one credential pair for beacon and execution endpoints.
type EthNodeInstanceConfig struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

type S3Config struct {
	Endpoint        string `yaml:"endpoint"`
	AccessKey       string `yaml:"access_key"`
	SecretKey       string `yaml:"secret_key"`
	Bucket          string `yaml:"bucket"`
	Region          string `yaml:"region,omitempty"`
	PublicURLPrefix string `yaml:"public_url_prefix,omitempty"`
}

type RateLimitConfig struct {
	Enabled           bool `yaml:"enabled"`
	RequestsPerMinute int  `yaml:"requests_per_minute,omitempty"`
	BurstSize         int  `yaml:"burst_size,omitempty"`
}

type AuditConfig struct {
	Enabled        bool `yaml:"enabled"`
	LogQueries     bool `yaml:"log_queries,omitempty"`
	MaxQueryLength int  `yaml:"max_query_length,omitempty"`
}

// MetricsConfig holds Prometheus metrics configuration for the proxy.
type MetricsConfig struct {
	// Enabled controls whether the Prometheus metrics server is active.
	Enabled bool `yaml:"enabled"`

	// Port is the port to serve the /metrics endpoint on (default: 9090).
	Port int `yaml:"port,omitempty"`
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

	// Metrics defaults.
	if c.Metrics.Port == 0 {
		c.Metrics.Port = 9090
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
	substituted, err := configutil.SubstituteEnvVars(string(data))
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
