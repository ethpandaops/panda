// Package github provides GitHub OAuth integration.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

// Error sentinels for GitHub OAuth operations.
var (
	// ErrGitHubOAuth indicates a failure in the GitHub OAuth flow.
	ErrGitHubOAuth = errors.New("GitHub OAuth error")

	// ErrGitHubAPI indicates a failure calling the GitHub API.
	ErrGitHubAPI = errors.New("GitHub API error")
)

const (
	// GitHub OAuth endpoints.
	githubAuthorizeURL = "https://github.com/login/oauth/authorize"
	githubTokenURL     = "https://github.com/login/oauth/access_token"
	githubAPIURL       = "https://api.github.com"

	// Default HTTP timeout.
	defaultTimeout = 30 * time.Second
)

// Client provides GitHub OAuth operations.
type Client struct {
	log          logrus.FieldLogger
	clientID     string
	clientSecret string
	httpClient   *http.Client
}

// NewClient creates a new GitHub OAuth client.
func NewClient(log logrus.FieldLogger, clientID, clientSecret string) *Client {
	return &Client{
		log:          log.WithField("component", "github_client"),
		clientID:     clientID,
		clientSecret: clientSecret,
		httpClient: &http.Client{
			Timeout: defaultTimeout,
		},
	}
}

// GetAuthorizationURL generates the GitHub OAuth authorization URL.
func (c *Client) GetAuthorizationURL(redirectURI, state, scope string) string {
	if scope == "" {
		scope = "read:user read:org"
	}

	params := url.Values{
		"client_id":    {c.clientID},
		"redirect_uri": {redirectURI},
		"scope":        {scope},
		"state":        {state},
		"allow_signup": {"false"},
	}

	authURL := fmt.Sprintf("%s?%s", githubAuthorizeURL, params.Encode())

	c.log.WithFields(logrus.Fields{
		"redirect_uri": redirectURI,
		"scope":        scope,
	}).Debug("Generated GitHub authorization URL")

	return authURL
}

// ExchangeCode exchanges an authorization code for an access token.
func (c *Client) ExchangeCode(ctx context.Context, code, redirectURI string) (*TokenResponse, error) {
	data := url.Values{
		"client_id":     {c.clientID},
		"client_secret": {c.clientSecret},
		"code":          {code},
		"redirect_uri":  {redirectURI},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, githubTokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("%w: creating request: %w", ErrGitHubOAuth, err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: exchanging code: %w", ErrGitHubOAuth, err)
	}

	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: reading response: %w", ErrGitHubOAuth, err)
	}

	if resp.StatusCode != http.StatusOK {
		c.log.WithFields(logrus.Fields{
			"status_code": resp.StatusCode,
			"response":    string(body),
		}).Error("GitHub token exchange failed")

		return nil, fmt.Errorf("%w: status %d", ErrGitHubOAuth, resp.StatusCode)
	}

	var tokenResp TokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("%w: parsing response: %w", ErrGitHubOAuth, err)
	}

	if tokenResp.Error != "" {
		c.log.WithFields(logrus.Fields{
			"error":       tokenResp.Error,
			"description": tokenResp.ErrorDescription,
		}).Error("GitHub OAuth error")

		return nil, fmt.Errorf("%w: %s: %s", ErrGitHubOAuth, tokenResp.Error, tokenResp.ErrorDescription)
	}

	return &tokenResp, nil
}

// GetUser fetches the user profile and organization memberships.
func (c *Client) GetUser(ctx context.Context, accessToken string) (*GitHubUser, error) {
	// Get user profile.
	userResp, err := c.getUserProfile(ctx, accessToken)
	if err != nil {
		return nil, err
	}

	// Get user organizations.
	orgs, err := c.getUserOrganizations(ctx, accessToken)
	if err != nil {
		return nil, fmt.Errorf("fetching user organizations: %w", err)
	}

	user := &GitHubUser{
		ID:            userResp.ID,
		Login:         userResp.Login,
		Name:          userResp.Name,
		Email:         userResp.Email,
		AvatarURL:     userResp.AvatarURL,
		Organizations: orgs,
	}

	c.log.WithFields(logrus.Fields{
		"github_id": user.ID,
		"login":     user.Login,
		"orgs":      orgs,
	}).Debug("Fetched GitHub user profile")

	return user, nil
}

// getUserProfile fetches the user's GitHub profile.
func (c *Client) getUserProfile(ctx context.Context, accessToken string) (*githubUserResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/user", githubAPIURL), nil)
	if err != nil {
		return nil, fmt.Errorf("%w: creating request: %w", ErrGitHubAPI, err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", accessToken))
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: fetching user: %w", ErrGitHubAPI, err)
	}

	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: reading response: %w", ErrGitHubAPI, err)
	}

	if resp.StatusCode != http.StatusOK {
		c.log.WithFields(logrus.Fields{
			"status_code": resp.StatusCode,
			"response":    string(body),
		}).Error("GitHub user API failed")

		return nil, fmt.Errorf("%w: status %d", ErrGitHubAPI, resp.StatusCode)
	}

	var userResp githubUserResponse
	if err := json.Unmarshal(body, &userResp); err != nil {
		return nil, fmt.Errorf("%w: parsing response: %w", ErrGitHubAPI, err)
	}

	return &userResp, nil
}

// getUserOrganizations fetches the user's organization memberships.
func (c *Client) getUserOrganizations(ctx context.Context, accessToken string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/user/orgs", githubAPIURL), nil)
	if err != nil {
		return nil, fmt.Errorf("%w: creating request: %w", ErrGitHubAPI, err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", accessToken))
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: fetching orgs: %w", ErrGitHubAPI, err)
	}

	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: reading response: %w", ErrGitHubAPI, err)
	}

	if resp.StatusCode != http.StatusOK {
		c.log.WithFields(logrus.Fields{
			"status_code": resp.StatusCode,
		}).Warn("Failed to fetch user organizations")

		return nil, fmt.Errorf("%w: status %d", ErrGitHubAPI, resp.StatusCode)
	}

	var orgsResp []githubOrgResponse
	if err := json.Unmarshal(body, &orgsResp); err != nil {
		return nil, fmt.Errorf("%w: parsing response: %w", ErrGitHubAPI, err)
	}

	orgs := make([]string, 0, len(orgsResp))
	for _, org := range orgsResp {
		orgs = append(orgs, org.Login)
	}

	return orgs, nil
}

// ValidateRedirectURI validates a redirect URI for security.
// Per OAuth 2.1 and MCP spec, redirect URIs must be either:
// - localhost (http://localhost:*, http://127.0.0.1:*, http://[::1]:*)
// - HTTPS URLs.
func ValidateRedirectURI(uri string) bool {
	parsed, err := url.Parse(uri)
	if err != nil {
		return false
	}

	// Check for localhost (allowed with HTTP).
	host := parsed.Hostname()
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return parsed.Scheme == "http" || parsed.Scheme == "https"
	}

	// Non-localhost must be HTTPS.
	if parsed.Scheme != "https" {
		return false
	}

	// Must have a valid host.
	return host != ""
}
