package handlers

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/sirupsen/logrus"
)

const (
	// githubAppJWTLifetime is how long the App JWT used for the token exchange
	// stays valid. GitHub rejects JWTs that live longer than 10 minutes.
	githubAppJWTLifetime = 9 * time.Minute
	// githubAppJWTClockSkew backdates the JWT issue time to tolerate clock
	// drift between this host and GitHub.
	githubAppJWTClockSkew = time.Minute
	// githubAppTokenRefreshMargin is how long before expiry a cached
	// installation token is replaced. Installation tokens last one hour.
	githubAppTokenRefreshMargin = 5 * time.Minute
)

// GitHubAppConfig holds the GitHub App credentials used to mint installation
// access tokens.
type GitHubAppConfig struct {
	AppID          int64
	InstallationID int64
	PrivateKey     *rsa.PrivateKey
}

// githubAppTokenSource mints installation access tokens for a GitHub App and
// caches them until shortly before they expire. Every token it mints is
// limited to the configured repositories and permissions.
type githubAppTokenSource struct {
	log          logrus.FieldLogger
	cfg          GitHubAppConfig
	apiBase      string
	repositories []string
	permissions  map[string]string
	httpClient   *http.Client
	now          func() time.Time

	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

func newGitHubAppTokenSource(
	log logrus.FieldLogger,
	cfg GitHubAppConfig,
	apiBase string,
	repositories []string,
	permissions map[string]string,
	httpClient *http.Client,
) *githubAppTokenSource {
	return &githubAppTokenSource{
		log:          log,
		cfg:          cfg,
		apiBase:      apiBase,
		repositories: repositories,
		permissions:  permissions,
		httpClient:   httpClient,
		now:          time.Now,
	}
}

// Token returns a cached installation token, minting a new one when none is
// cached or the cached one expires within githubAppTokenRefreshMargin. If
// minting fails while the cached token is still valid, the cached token is
// returned.
func (s *githubAppTokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.token != "" && s.now().Before(s.expiresAt.Add(-githubAppTokenRefreshMargin)) {
		return s.token, nil
	}

	token, expiresAt, err := s.mint(ctx)
	if err != nil {
		if s.token != "" && s.now().Before(s.expiresAt) {
			s.log.WithError(err).Warn("Refreshing GitHub App installation token failed; using cached token")

			return s.token, nil
		}

		return "", err
	}

	s.token = token
	s.expiresAt = expiresAt

	return token, nil
}

// appJWT returns a JWT that authenticates as the App itself.
func (s *githubAppTokenSource) appJWT() (string, error) {
	now := s.now()

	claims := jwt.RegisteredClaims{
		Issuer:    strconv.FormatInt(s.cfg.AppID, 10),
		IssuedAt:  jwt.NewNumericDate(now.Add(-githubAppJWTClockSkew)),
		ExpiresAt: jwt.NewNumericDate(now.Add(githubAppJWTLifetime)),
	}

	signed, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(s.cfg.PrivateKey)
	if err != nil {
		return "", fmt.Errorf("signing app JWT: %w", err)
	}

	return signed, nil
}

// mint exchanges an App JWT for a scoped installation access token.
func (s *githubAppTokenSource) mint(ctx context.Context) (string, time.Time, error) {
	appJWT, err := s.appJWT()
	if err != nil {
		return "", time.Time{}, err
	}

	body, err := json.Marshal(map[string]any{
		"repositories": s.repositories,
		"permissions":  s.permissions,
	})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("encoding token request: %w", err)
	}

	url := fmt.Sprintf("%s/app/installations/%d/access_tokens", s.apiBase, s.cfg.InstallationID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("creating token request: %w", err)
	}

	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+appJWT)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("requesting installation token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

		return "", time.Time{}, fmt.Errorf(
			"github returned status %d for installation token: %s", resp.StatusCode, string(respBody),
		)
	}

	var result struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", time.Time{}, fmt.Errorf("decoding installation token: %w", err)
	}

	if result.Token == "" {
		return "", time.Time{}, fmt.Errorf("github returned an empty installation token")
	}

	return result.Token, result.ExpiresAt, nil
}
