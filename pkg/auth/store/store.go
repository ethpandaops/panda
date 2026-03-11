// Package store provides local credential storage for OAuth tokens.
package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/auth/client"
)

// Store manages local credential storage.
type Store interface {
	// Path returns the resolved credentials file path.
	Path() string

	// Save saves tokens to the store.
	Save(tokens *client.Tokens) error

	// Load loads tokens from the store.
	Load() (*client.Tokens, error)

	// Clear removes stored tokens.
	Clear() error

	// GetAccessToken returns a valid access token, refreshing if needed.
	GetAccessToken() (string, error)

	// IsAuthenticated returns true if stored credentials are still valid or can
	// be refreshed into a valid access token.
	IsAuthenticated() bool
}

// Config configures the credential store.
type Config struct {
	// Path is the path to the credentials file.
	// Defaults to a namespaced file in ~/.config/panda/credentials/
	Path string

	// IssuerURL namespaces stored credentials by auth issuer.
	IssuerURL string

	// ClientID namespaces stored credentials by OAuth client.
	ClientID string

	// Resource namespaces stored credentials by requested resource.
	Resource string

	// RefreshBuffer is how long before expiry to refresh the token.
	RefreshBuffer time.Duration

	// AuthClient is the OAuth client for refreshing tokens.
	AuthClient client.Client
}

// store implements the Store interface.
type store struct {
	log    logrus.FieldLogger
	cfg    Config
	mu     sync.RWMutex
	tokens *client.Tokens
}

// New creates a new credential store.
func New(log logrus.FieldLogger, cfg Config) Store {
	if cfg.Path == "" {
		cfg.Path = defaultCredentialPath(cfg)
	}

	if cfg.RefreshBuffer == 0 {
		cfg.RefreshBuffer = 5 * time.Minute
	}

	return &store{
		log: log.WithField("component", "credential-store"),
		cfg: cfg,
	}
}

func (s *store) Path() string {
	return s.cfg.Path
}

// Save saves tokens to the store.
func (s *store) Save(tokens *client.Tokens) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Ensure directory exists.
	dir := filepath.Dir(s.cfg.Path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	// Marshal tokens.
	data, err := json.MarshalIndent(tokens, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling tokens: %w", err)
	}

	// Write file with secure permissions.
	if err := os.WriteFile(s.cfg.Path, data, 0600); err != nil {
		return fmt.Errorf("writing credentials file: %w", err)
	}

	s.tokens = tokens
	s.log.Debug("Saved credentials")

	return nil
}

// Load loads tokens from the store.
func (s *store) Load() (*client.Tokens, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if file exists.
	if _, err := os.Stat(s.cfg.Path); os.IsNotExist(err) {
		return nil, nil
	}

	data, err := os.ReadFile(s.cfg.Path)
	if err != nil {
		return nil, fmt.Errorf("reading credentials file: %w", err)
	}

	var tokens client.Tokens
	if err := json.Unmarshal(data, &tokens); err != nil {
		return nil, fmt.Errorf("unmarshaling tokens: %w", err)
	}

	s.tokens = &tokens

	return &tokens, nil
}

// Clear removes stored tokens.
func (s *store) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.Remove(s.cfg.Path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing credentials file: %w", err)
	}

	s.tokens = nil
	s.log.Debug("Cleared credentials")

	return nil
}

// GetAccessToken returns a valid access token, refreshing if needed.
func (s *store) GetAccessToken() (string, error) {
	tokens, err := s.getTokens()
	if err != nil {
		return "", fmt.Errorf("loading tokens: %w", err)
	}
	if tokens == nil {
		return "", fmt.Errorf("not authenticated")
	}

	// Check if token needs refresh.
	if s.needsRefresh(tokens) {
		if tokens.RefreshToken == "" {
			if time.Now().Before(tokens.ExpiresAt) {
				return tokens.AccessToken, nil
			}

			return "", fmt.Errorf("access token expired and no refresh token available")
		}

		newTokens, err := s.refresh(tokens.RefreshToken)
		if err != nil {
			if time.Now().Before(tokens.ExpiresAt) {
				return tokens.AccessToken, nil
			}

			return "", fmt.Errorf("refreshing token: %w", err)
		}
		tokens = newTokens
	}

	return tokens.AccessToken, nil
}

// IsAuthenticated returns true if stored credentials are still valid or can be refreshed.
func (s *store) IsAuthenticated() bool {
	tokens, err := s.getTokens()
	if err != nil {
		return false
	}
	if tokens == nil {
		return false
	}

	if time.Now().Before(tokens.ExpiresAt) {
		return true
	}

	return tokens.RefreshToken != "" && s.cfg.AuthClient != nil
}

// getTokens returns cached tokens or loads them from disk.
func (s *store) getTokens() (*client.Tokens, error) {
	s.mu.RLock()
	tokens := s.tokens
	s.mu.RUnlock()
	if tokens != nil {
		return tokens, nil
	}

	return s.Load()
}

// needsRefresh returns true if the token should be refreshed.
func (s *store) needsRefresh(tokens *client.Tokens) bool {
	if tokens == nil {
		return true
	}

	// Refresh if within buffer of expiry.
	return time.Now().Add(s.cfg.RefreshBuffer).After(tokens.ExpiresAt)
}

// refresh refreshes the access token.
func (s *store) refresh(refreshToken string) (*client.Tokens, error) {
	if s.cfg.AuthClient == nil {
		return nil, fmt.Errorf("no auth client configured for refresh")
	}

	if refreshToken == "" {
		return nil, fmt.Errorf("no refresh token available")
	}

	s.log.Debug("Refreshing access token")

	newTokens, err := s.cfg.AuthClient.Refresh(context.Background(), refreshToken)
	if err != nil {
		return nil, fmt.Errorf("refreshing token: %w", err)
	}

	// Save new tokens.
	if err := s.Save(newTokens); err != nil {
		return nil, err
	}

	return newTokens, nil
}

func defaultCredentialPath(cfg Config) string {
	home, _ := os.UserHomeDir()
	newBaseDir := filepath.Join(home, ".config", "panda")
	oldBaseDir := filepath.Join(home, ".config", "ethpandaops-mcp")

	key := credentialNamespaceKey(cfg.IssuerURL, cfg.ClientID, cfg.Resource)

	var newPath, oldPath string
	if key == "" {
		newPath = filepath.Join(newBaseDir, "credentials.json")
		oldPath = filepath.Join(oldBaseDir, "credentials.json")
	} else {
		newPath = filepath.Join(newBaseDir, "credentials", key+".json")
		oldPath = filepath.Join(oldBaseDir, "credentials", key+".json")
	}

	// Migrate: if old credentials exist but new ones don't, fall back to old path.
	if _, err := os.Stat(newPath); os.IsNotExist(err) {
		if _, err := os.Stat(oldPath); err == nil {
			logrus.Warnf(
				"Using legacy credential path %s; move it to %s",
				oldPath, newPath,
			)

			return oldPath
		}
	}

	return newPath
}

func credentialNamespaceKey(issuerURL, clientID, resource string) string {
	normalized := strings.Join([]string{
		strings.TrimSpace(strings.TrimRight(issuerURL, "/")),
		strings.TrimSpace(clientID),
		strings.TrimSpace(strings.TrimRight(resource, "/")),
	}, "\n")

	if strings.TrimSpace(strings.ReplaceAll(normalized, "\n", "")) == "" {
		return ""
	}

	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:8])
}
