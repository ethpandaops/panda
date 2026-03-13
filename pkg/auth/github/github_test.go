package github

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGitHubUserIsMemberOf(t *testing.T) {
	t.Parallel()

	user := &GitHubUser{
		Organizations: []string{"ethpandaops", "openai"},
	}

	assert.True(t, user.IsMemberOf(nil))
	assert.True(t, user.IsMemberOf([]string{"openai"}))
	assert.False(t, user.IsMemberOf([]string{"other"}))
}

func TestValidateRedirectURI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		uri  string
		want bool
	}{
		{name: "localhost http", uri: "http://localhost:2480/callback", want: true},
		{name: "loopback https", uri: "https://127.0.0.1/callback", want: true},
		{name: "ipv6 localhost", uri: "http://[::1]:2480/callback", want: true},
		{name: "remote https", uri: "https://example.com/callback", want: true},
		{name: "remote http rejected", uri: "http://example.com/callback", want: false},
		{name: "missing host rejected", uri: "https:///callback", want: false},
		{name: "invalid uri rejected", uri: "://bad", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, ValidateRedirectURI(tt.uri))
		})
	}
}

func TestGetAuthorizationURLUsesDefaultScope(t *testing.T) {
	t.Parallel()

	client := NewClient(logrus.New(), "client-id", "secret")

	rawURL := client.GetAuthorizationURL("http://localhost:2480/callback", "state-123", "")
	parsed, err := url.Parse(rawURL)
	require.NoError(t, err)

	query := parsed.Query()
	assert.Equal(t, githubAuthorizeURL, parsed.Scheme+"://"+parsed.Host+parsed.Path)
	assert.Equal(t, "client-id", query.Get("client_id"))
	assert.Equal(t, "http://localhost:2480/callback", query.Get("redirect_uri"))
	assert.Equal(t, "read:user read:org", query.Get("scope"))
	assert.Equal(t, "state-123", query.Get("state"))
	assert.Equal(t, "false", query.Get("allow_signup"))
}

func TestExchangeCodeSuccess(t *testing.T) {
	t.Parallel()

	client := NewClient(logrus.New(), "client-id", "client-secret")
	client.SetHTTPClient(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			assert.Equal(t, http.MethodPost, req.Method)
			assert.Equal(t, githubOAuthExchangeURL, req.URL.String())
			assert.Equal(t, "application/json", req.Header.Get("Accept"))
			assert.Equal(t, "application/x-www-form-urlencoded", req.Header.Get("Content-Type"))

			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)

			values, err := url.ParseQuery(string(body))
			require.NoError(t, err)
			assert.Equal(t, "client-id", values.Get("client_id"))
			assert.Equal(t, "client-secret", values.Get("client_secret"))
			assert.Equal(t, "code-123", values.Get("code"))
			assert.Equal(t, "http://localhost/callback", values.Get("redirect_uri"))

			return jsonResponse(http.StatusOK, `{"access_token":"token-123","token_type":"bearer","scope":"read:user"}`), nil
		}),
	})

	token, err := client.ExchangeCode(context.Background(), "code-123", "http://localhost/callback")
	require.NoError(t, err)
	assert.Equal(t, "token-123", token.AccessToken)
	assert.Equal(t, "bearer", token.TokenType)
	assert.Equal(t, "read:user", token.Scope)
}

func TestExchangeCodeReturnsOAuthErrorPayload(t *testing.T) {
	t.Parallel()

	client := NewClient(logrus.New(), "client-id", "client-secret")
	client.SetHTTPClient(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusOK, `{"error":"bad_verification_code","error_description":"bad code"}`), nil
		}),
	})

	_, err := client.ExchangeCode(context.Background(), "bad-code", "http://localhost/callback")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrGitHubOAuth)
	assert.Contains(t, err.Error(), "bad_verification_code")
}

func TestGetUserAggregatesProfileAndOrganizations(t *testing.T) {
	t.Parallel()

	client := NewClient(logrus.New(), "client-id", "client-secret")
	client.SetHTTPClient(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			assert.Equal(t, "Bearer token-123", req.Header.Get("Authorization"))
			assert.Equal(t, "application/vnd.github+json", req.Header.Get("Accept"))
			assert.Equal(t, "2022-11-28", req.Header.Get("X-GitHub-Api-Version"))

			switch req.URL.String() {
			case githubAPIURL + "/user":
				return jsonResponse(http.StatusOK, `{"id":42,"login":"octocat","name":"Octo Cat","email":"octo@example.com","avatar_url":"https://example.com/avatar.png"}`), nil
			case githubAPIURL + "/user/orgs":
				return jsonResponse(http.StatusOK, `[{"login":"ethpandaops"},{"login":"openai"}]`), nil
			default:
				t.Fatalf("unexpected URL: %s", req.URL.String())
				return nil, nil
			}
		}),
	})

	user, err := client.GetUser(context.Background(), "token-123")
	require.NoError(t, err)
	assert.Equal(t, int64(42), user.ID)
	assert.Equal(t, "octocat", user.Login)
	assert.Equal(t, "Octo Cat", user.Name)
	assert.Equal(t, "octo@example.com", user.Email)
	assert.Equal(t, "https://example.com/avatar.png", user.AvatarURL)
	assert.Equal(t, []string{"ethpandaops", "openai"}, user.Organizations)
}

func TestGetUserWrapsOrganizationLookupErrors(t *testing.T) {
	t.Parallel()

	client := NewClient(logrus.New(), "client-id", "client-secret")
	client.SetHTTPClient(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.String() {
			case githubAPIURL + "/user":
				return jsonResponse(http.StatusOK, `{"id":42,"login":"octocat"}`), nil
			case githubAPIURL + "/user/orgs":
				return jsonResponse(http.StatusForbidden, `{"message":"forbidden"}`), nil
			default:
				t.Fatalf("unexpected URL: %s", req.URL.String())
				return nil, nil
			}
		}),
	})

	_, err := client.GetUser(context.Background(), "token-123")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrGitHubAPI)
	assert.Contains(t, err.Error(), "fetching user organizations")
}

type roundTripFunc func(req *http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}
