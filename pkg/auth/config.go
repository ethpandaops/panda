package auth

import "time"

// Config holds OAuth server configuration for a local product edge.
type Config struct {
	Enabled         bool               `yaml:"enabled"`
	IssuerURL       string             `yaml:"issuer_url,omitempty"`
	GitHub          *GitHubConfig      `yaml:"github,omitempty"`
	AllowedOrgs     []string           `yaml:"allowed_orgs,omitempty"`
	Tokens          TokensConfig       `yaml:"tokens"`
	AccessTokenTTL  time.Duration      `yaml:"access_token_ttl,omitempty"`
	RefreshTokenTTL time.Duration      `yaml:"refresh_token_ttl,omitempty"`
	SuccessPage     *SuccessPageConfig `yaml:"success_page,omitempty"`
}

// GitHubConfig holds GitHub OAuth configuration.
type GitHubConfig struct {
	ClientID     string `yaml:"client_id"`
	ClientSecret string `yaml:"client_secret"`
}

// TokensConfig holds signed access token configuration.
type TokensConfig struct {
	SecretKey string `yaml:"secret_key"`
}
