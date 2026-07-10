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
	defaultServerPort                   = 2480
	defaultProxyName                    = "primary"
	defaultProxyURL                     = "http://localhost:18081"
	defaultLocalProxyClickHouseName     = "local-kurtosis"
	defaultLocalProxyClickHouseDesc     = "Local Kurtosis devnet logs (OpenTelemetry, autodiscovered)"
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

	// Uploads gates the `panda upload` surface (the upload/publish API and the
	// /u/ preview pages). Unset means enabled; set it to false to opt out.
	// Nothing leaves the machine without an explicit publish either way.
	Uploads *bool `yaml:"uploads,omitempty"`

	// Deprecated: Transport is accepted for backwards compatibility but ignored.
	// The server always runs HTTP with both SSE and streamable-http transports.
	Transport string `yaml:"transport,omitempty"`
}

// UploadsEnabled reports whether the `panda upload` surface is on (the default
// when server.uploads is unset).
func (c ServerConfig) UploadsEnabled() bool {
	return c.Uploads == nil || *c.Uploads
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

	// PythonPath pins the Python interpreter the direct backend invokes for
	// untrusted sandbox code. Empty falls back to python3/python on PATH. Point
	// this at the baked sandbox venv (e.g. /usr/local/bin/python3) so code runs
	// against a known, dependency-complete environment rather than ambient PATH.
	PythonPath string `yaml:"python_path,omitempty"`

	// ExecUID/ExecGID are the unprivileged uid/gid the direct backend drops to
	// when running untrusted Python. They MUST differ from the server's own uid,
	// so the server's config, credentials, and /proc are unreadable by the
	// executed code. Zero means unset; the direct backend fails closed at startup
	// unless both are set to a non-zero id distinct from the server's uid.
	ExecUID int `yaml:"exec_uid,omitempty"`
	ExecGID int `yaml:"exec_gid,omitempty"`

	// RuntimeSocket is the unix-domain socket the server serves its runtime API
	// on for the direct backend. That backend runs untrusted Python in an empty
	// network namespace with no route out, so the sandbox reaches the server over
	// this socket instead of TCP — making network exfiltration impossible rather
	// than merely disallowed. Only the direct backend uses it; docker/gvisor
	// sandboxes use the TCP API over the sandbox network. Defaults to
	// <TMPDIR>/panda-sandbox-runtime.sock; see RuntimeSocketPath.
	RuntimeSocket string `yaml:"runtime_socket,omitempty"`

	// WorkspaceDir is the parent directory the direct backend creates per-execution
	// and per-session workspaces under. It is kept off shared /tmp and locked to
	// the server + exec uid, so untrusted code's script and scratch files are
	// unreadable to other users on the host. Defaults to
	// <TMPDIR>/panda-sandbox-workspaces; see WorkspaceRoot.
	WorkspaceDir string `yaml:"workspace_dir,omitempty"`

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
	// MetricsAddr is the full address (host:port) to serve metrics on. When set,
	// it takes precedence over MetricsPort.
	MetricsAddr string `yaml:"metrics_addr,omitempty"`
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
	// "oidc" is an external OpenID Connect issuer such as Authentik, and
	// "client_credentials" is the non-interactive service-account flow: access
	// tokens are minted from the issuer's token endpoint using Username +
	// Password (Authentik service-account form), cached in memory, and
	// re-minted before expiry. No credential files are written.
	Mode string `yaml:"mode,omitempty"`

	// IssuerURL is the OAuth issuer URL for proxy authentication.
	IssuerURL string `yaml:"issuer_url"`

	// ClientID is the OAuth client ID for authentication.
	ClientID string `yaml:"client_id"`

	// Username is the service-account username for mode "client_credentials".
	Username string `yaml:"username,omitempty"`

	// Password is the service-account app password for mode "client_credentials".
	// Use ${ENV_VAR} substitution to source it from the environment.
	Password string `yaml:"password,omitempty"`

	// Resource is the optional OAuth resource indicator to request.
	// Leave empty for standard OIDC providers that do not use RFC 8707 resource parameters.
	Resource string `yaml:"resource,omitempty"`

	// Scopes are the OAuth scopes requested at login. When empty, the auth client
	// requests its defaults (openid, email, groups, offline_access). When set,
	// this list is sent verbatim — so include those base scopes plus any extras.
	// For example, requesting the workflow-engine audience makes Authentik
	// cross-grant that audience to the panda-proxy token so the same credential
	// works against the workflow engine in passthrough mode. Omitting
	// offline_access means no refresh token is issued.
	Scopes []string `yaml:"scopes,omitempty"`

	// RefreshTokenTTL is the expected lifetime of the refresh token issued by the
	// OIDC provider. When set, the client will proactively refresh at 50% of this
	// duration to keep the refresh token alive via provider rotation.
	RefreshTokenTTL time.Duration `yaml:"refresh_token_ttl,omitempty"`
}

// ResolvedAuthIssuerURL returns the OIDC issuer used for this proxy's auth: the
// explicit issuer_url, or the proxy URL when unset. It is the single source of
// truth so the proxy token source and the server's credential controller derive
// the same credential file.
func (p ProxyConfig) ResolvedAuthIssuerURL() string {
	if p.Auth == nil {
		return ""
	}

	issuer := strings.TrimSpace(p.Auth.IssuerURL)
	if issuer == "" {
		issuer = p.URL
	}

	return strings.TrimRight(strings.TrimSpace(issuer), "/")
}

// ResolvedAuthResource returns the RFC 8707 resource used for this proxy's auth,
// applying the same defaulting the credential store keys on: empty for external
// issuers (oidc, client_credentials), and the proxy URL for the legacy embedded
// oauth issuer. Kept in lockstep with ResolvedAuthIssuerURL so credential paths
// never diverge between the proxy client and the server's auth controller.
func (p ProxyConfig) ResolvedAuthResource() string {
	if p.Auth == nil {
		return ""
	}

	resource := strings.TrimSpace(p.Auth.Resource)

	mode := strings.TrimSpace(p.Auth.Mode)
	if resource == "" && mode != "oidc" && mode != "client_credentials" {
		resource = p.URL
	}

	return strings.TrimRight(strings.TrimSpace(resource), "/")
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
	// Contains declares the datasets stored in this datasource, mirroring the
	// standalone proxy's contains entries. Passed through to discovery verbatim.
	Contains []LocalProxyDatasetBinding `yaml:"contains,omitempty"`
}

// LocalProxyDatasetBinding declares a dataset stored in a local datasource.
// Dataset names a knowledge pack shipped with the release; Params are opaque
// placement hints interpreted by that pack; Notes says what distinguishes this
// copy from the dataset's other copies.
type LocalProxyDatasetBinding struct {
	Dataset string            `yaml:"dataset"`
	Params  map[string]string `yaml:"params,omitempty"`
	Notes   string            `yaml:"notes,omitempty"`
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

// BaseUsesProxyList reports whether the base config file declares a non-empty
// proxies list. User overrides are intentionally ignored.
func BaseUsesProxyList(path string) (bool, error) {
	resolvedPath, err := configpath.ResolveAppConfigPath(path)
	if err != nil {
		return false, err
	}

	data, err := os.ReadFile(resolvedPath)
	if err != nil {
		return false, fmt.Errorf("reading config file %s: %w", resolvedPath, err)
	}

	substituted, err := substituteEnvVars(string(data))
	if err != nil {
		return false, fmt.Errorf("substituting env vars: %w", err)
	}

	var raw map[string]any
	if err := yaml.Unmarshal([]byte(substituted), &raw); err != nil {
		return false, fmt.Errorf("parsing config: %w", err)
	}

	proxies, ok := raw["proxies"].([]any)
	return ok && len(proxies) > 0, nil
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
		cfg.Server.Port = defaultServerPort
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
		cfg.Sandbox.Sessions.MaxSessions = 50
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
		Contains: []LocalProxyDatasetBinding{
			{
				Dataset: "otel-logs",
				Params:  map[string]string{"database": defaultLocalProxyClickHouseDatabase},
			},
		},
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

// SandboxBackendNone disables the sandbox: the server runs without a container
// runtime and execute_python is unavailable. Kept in sync with
// sandbox.BackendNone, which config cannot reference without an import cycle.
const SandboxBackendNone = "none"

// SandboxBackendDirect runs Python as a confined subprocess of the server rather
// than in a container. Kept in sync with sandbox.BackendDirect, which config
// cannot reference without an import cycle.
const SandboxBackendDirect = "direct"

// defaultRuntimeSocketName is the runtime API socket filename used when the
// direct backend is active and sandbox.runtime_socket is not set.
const defaultRuntimeSocketName = "panda-sandbox-runtime.sock"

// defaultWorkspaceDirName is the workspace root directory name used when the
// direct backend is active and sandbox.workspace_dir is not set.
const defaultWorkspaceDirName = "panda-sandbox-workspaces"

// WorkspaceRoot returns the parent directory the direct backend places its
// per-execution and per-session workspaces under. Falls back to
// <TMPDIR>/panda-sandbox-workspaces.
func (c SandboxConfig) WorkspaceRoot() string {
	if p := strings.TrimSpace(c.WorkspaceDir); p != "" {
		return p
	}

	return filepath.Join(os.TempDir(), defaultWorkspaceDirName)
}

// RuntimeSocketPath returns the unix-domain socket the server serves the runtime
// API on, or "" when the socket is not in use. Only the direct backend uses it:
// its Python runs in an empty network namespace and reaches the server here
// rather than over TCP. Falls back to <TMPDIR>/panda-sandbox-runtime.sock.
func (c SandboxConfig) RuntimeSocketPath() string {
	if c.Backend != SandboxBackendDirect {
		return ""
	}

	if p := strings.TrimSpace(c.RuntimeSocket); p != "" {
		return p
	}

	return filepath.Join(os.TempDir(), defaultRuntimeSocketName)
}

// Validate validates the configuration.
func (c *Config) Validate() error {
	// Only docker and gvisor pull a sandbox image: "none" has no container
	// runtime, and "direct" runs code as a subprocess of the server.
	if c.Sandbox.Backend != SandboxBackendNone && c.Sandbox.Backend != SandboxBackendDirect && c.Sandbox.Image == "" {
		return errors.New("sandbox.image is required for docker/gvisor backends")
	}

	// The direct backend executes untrusted Python in-process. It only provides
	// isolation when it drops to a dedicated unprivileged uid/gid that owns none
	// of the server's secrets, so require both and refuse to run as the server's
	// own uid (fail closed — see pkg/sandbox/direct_harden_linux.go).
	if c.Sandbox.Backend == SandboxBackendDirect {
		if c.Sandbox.ExecUID <= 0 || c.Sandbox.ExecGID <= 0 {
			return errors.New("sandbox.exec_uid and sandbox.exec_gid are required (non-zero) for the direct backend")
		}
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

		if proxy.Auth != nil && strings.TrimSpace(proxy.Auth.Mode) == "client_credentials" {
			if strings.TrimSpace(proxy.Auth.IssuerURL) == "" {
				return fmt.Errorf("proxies[%d].auth.issuer_url is required for mode client_credentials", i)
			}

			if strings.TrimSpace(proxy.Auth.ClientID) == "" {
				return fmt.Errorf("proxies[%d].auth.client_id is required for mode client_credentials", i)
			}

			if strings.TrimSpace(proxy.Auth.Username) == "" {
				return fmt.Errorf("proxies[%d].auth.username is required for mode client_credentials", i)
			}

			if strings.TrimSpace(proxy.Auth.Password) == "" {
				return fmt.Errorf("proxies[%d].auth.password is required for mode client_credentials", i)
			}
		}
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
