package auth

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSimpleServiceLifecycleAndMetadataRoutes(t *testing.T) {
	t.Parallel()

	disabled, err := NewSimpleService(logrus.New(), Config{})
	require.NoError(t, err)
	assert.False(t, disabled.Enabled())
	require.NoError(t, disabled.Start(context.Background()))
	require.NoError(t, disabled.Stop())

	disabledRouter := chi.NewRouter()
	disabled.MountRoutes(disabledRouter)
	disabledRecorder := httptest.NewRecorder()
	disabledRouter.ServeHTTP(disabledRecorder, httptest.NewRequest(
		http.MethodGet,
		"http://proxy.test/.well-known/oauth-protected-resource",
		nil,
	))
	assert.Equal(t, http.StatusNotFound, disabledRecorder.Code)

	_, err = NewSimpleService(logrus.New(), Config{
		Enabled: true,
		Tokens:  TokensConfig{SecretKey: "secret"},
	})
	require.ErrorContains(t, err, "github configuration is required")

	_, err = NewSimpleService(logrus.New(), Config{
		Enabled: true,
		GitHub: &GitHubConfig{
			ClientID:     "github-client",
			ClientSecret: "github-secret",
		},
	})
	require.ErrorContains(t, err, "tokens.secret_key is required")

	svc := newTestSimpleService(t)
	assert.True(t, svc.Enabled())
	require.NoError(t, svc.Start(context.Background()))
	require.NoError(t, svc.Stop())

	router := chi.NewRouter()
	svc.MountRoutes(router)

	resourceReq := httptest.NewRequest(
		http.MethodGet,
		"http://internal/.well-known/oauth-protected-resource",
		nil,
	)
	resourceReq.Host = "internal"
	resourceReq.Header.Set("X-Forwarded-Proto", "https")
	resourceReq.Header.Set("X-Forwarded-Host", "proxy.example")

	resourceRecorder := httptest.NewRecorder()
	router.ServeHTTP(resourceRecorder, resourceReq)
	require.Equal(t, http.StatusOK, resourceRecorder.Code)
	assert.Equal(t, "application/json", resourceRecorder.Header().Get("Content-Type"))
	assert.Equal(t, "max-age=3600", resourceRecorder.Header().Get("Cache-Control"))

	var resourceMetadata map[string]any
	require.NoError(t, json.Unmarshal(resourceRecorder.Body.Bytes(), &resourceMetadata))
	assert.Equal(t, "https://proxy.example", resourceMetadata["resource"])

	serverReq := httptest.NewRequest(
		http.MethodGet,
		"https://proxy.example/.well-known/oauth-authorization-server",
		nil,
	)
	serverRecorder := httptest.NewRecorder()
	router.ServeHTTP(serverRecorder, serverReq)
	require.Equal(t, http.StatusOK, serverRecorder.Code)

	var serverMetadata map[string]any
	require.NoError(t, json.Unmarshal(serverRecorder.Body.Bytes(), &serverMetadata))
	assert.Equal(t, "https://proxy.example", serverMetadata["issuer"])
	assert.Equal(t, "https://proxy.example/auth/authorize", serverMetadata["authorization_endpoint"])
	assert.Equal(t, "https://proxy.example/auth/token", serverMetadata["token_endpoint"])
}

func TestHandleAuthorizeValidationAndSuccess(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		rawQuery    string
		wantCode    int
		wantErrCode string
		wantMessage string
	}{
		{
			name:        "invalid challenge method",
			rawQuery:    "client_id=panda&redirect_uri=http://localhost:8085/callback&code_challenge=challenge&code_challenge_method=plain&resource=http://proxy.test",
			wantCode:    http.StatusBadRequest,
			wantErrCode: "invalid_request",
			wantMessage: "code_challenge_method must be S256",
		},
		{
			name:        "missing challenge",
			rawQuery:    "client_id=panda&redirect_uri=http://localhost:8085/callback&code_challenge_method=S256&resource=http://proxy.test",
			wantCode:    http.StatusBadRequest,
			wantErrCode: "invalid_request",
			wantMessage: "code_challenge is required",
		},
		{
			name:        "missing resource",
			rawQuery:    "client_id=panda&redirect_uri=http://localhost:8085/callback&code_challenge=challenge&code_challenge_method=S256",
			wantCode:    http.StatusBadRequest,
			wantErrCode: "invalid_request",
			wantMessage: "resource is required (RFC 8707)",
		},
		{
			name:        "missing redirect URI",
			rawQuery:    "client_id=panda&code_challenge=challenge&code_challenge_method=S256&resource=http://proxy.test",
			wantCode:    http.StatusBadRequest,
			wantErrCode: "invalid_request",
			wantMessage: "redirect_uri is required",
		},
		{
			name:        "invalid redirect URI",
			rawQuery:    "client_id=panda&redirect_uri=http://example.com/callback&code_challenge=challenge&code_challenge_method=S256&resource=http://proxy.test",
			wantCode:    http.StatusBadRequest,
			wantErrCode: "invalid_request",
			wantMessage: "invalid redirect_uri",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc := newTestSimpleService(t)
			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "http://proxy.test/auth/authorize?"+tc.rawQuery, nil)

			svc.handleAuthorize(recorder, req)
			require.Equal(t, tc.wantCode, recorder.Code)

			var payload map[string]string
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
			assert.Equal(t, tc.wantErrCode, payload["error"])
			assert.Equal(t, tc.wantMessage, payload["error_description"])
		})
	}

	svc := newTestSimpleService(t)
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodGet,
		"http://proxy.test/auth/authorize?client_id=panda&redirect_uri=http://localhost:8085/callback&code_challenge=challenge&code_challenge_method=S256&resource=http://proxy.test&state=local-state",
		nil,
	)

	svc.handleAuthorize(recorder, req)
	require.Equal(t, http.StatusFound, recorder.Code)

	redirectURL, err := url.Parse(recorder.Header().Get("Location"))
	require.NoError(t, err)
	query := redirectURL.Query()
	assert.Equal(t, "https://github.com/login/oauth/authorize", redirectURL.Scheme+"://"+redirectURL.Host+redirectURL.Path)
	assert.Equal(t, "github-client", query.Get("client_id"))
	assert.Equal(t, "http://proxy.test/auth/callback", query.Get("redirect_uri"))
	assert.Equal(t, "read:user read:org", query.Get("scope"))
	assert.Equal(t, "false", query.Get("allow_signup"))

	githubState := query.Get("state")
	require.NotEmpty(t, githubState)

	svc.pendingMu.RLock()
	pending := svc.pending[githubState]
	svc.pendingMu.RUnlock()
	require.NotNil(t, pending)
	assert.Equal(t, "panda", pending.ClientID)
	assert.Equal(t, "http://localhost:8085/callback", pending.RedirectURI)
	assert.Equal(t, "challenge", pending.CodeChallenge)
	assert.Equal(t, "http://proxy.test", pending.Resource)
	assert.Equal(t, "local-state", pending.State)
}

func TestHandleCallbackScenarios(t *testing.T) {
	t.Parallel()

	t.Run("missing code or state", func(t *testing.T) {
		t.Parallel()

		svc := newTestSimpleService(t)
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://proxy.test/auth/callback?state=github-state", nil)

		svc.handleCallback(recorder, req)
		require.Equal(t, http.StatusBadRequest, recorder.Code)
		assert.Contains(t, recorder.Body.String(), "missing code or state")
	})

	t.Run("invalid state", func(t *testing.T) {
		t.Parallel()

		svc := newTestSimpleService(t)
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://proxy.test/auth/callback?code=code-123&state=missing", nil)

		svc.handleCallback(recorder, req)
		require.Equal(t, http.StatusBadRequest, recorder.Code)
		assert.Contains(t, recorder.Body.String(), "invalid or expired state")
	})

	t.Run("github exchange failure", func(t *testing.T) {
		t.Parallel()

		svc := newTestSimpleService(t)
		svc.storePendingAuth("github-state", &pendingAuth{
			ClientID:      "panda",
			RedirectURI:   "http://localhost:8085/callback",
			CodeChallenge: "challenge",
			Resource:      "http://proxy.test",
			CreatedAt:     time.Now(),
		})
		svc.github.SetHTTPClient(&http.Client{
			Transport: authRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				assert.Equal(t, "https://github.com/login/oauth/access_token", req.URL.String())
				return authJSONResponse(http.StatusBadGateway, `{"error":"bad_gateway"}`), nil
			}),
		})

		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://proxy.test/auth/callback?code=code-123&state=github-state", nil)

		svc.handleCallback(recorder, req)
		require.Equal(t, http.StatusBadRequest, recorder.Code)
		assert.Contains(t, recorder.Body.String(), "Authentication Failed")
	})

	t.Run("user outside allowed orgs is denied", func(t *testing.T) {
		t.Parallel()

		svc := newTestSimpleService(t)
		svc.allowedOrgs = []string{"ethpandaops"}
		svc.storePendingAuth("github-state", &pendingAuth{
			ClientID:      "panda",
			RedirectURI:   "http://localhost:8085/callback",
			CodeChallenge: "challenge",
			Resource:      "http://proxy.test",
			CreatedAt:     time.Now(),
		})
		svc.github.SetHTTPClient(&http.Client{
			Transport: authRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.String() {
				case "https://github.com/login/oauth/access_token":
					return authJSONResponse(http.StatusOK, `{"access_token":"token-123","token_type":"bearer"}`), nil
				case "https://api.github.com/user":
					return authJSONResponse(http.StatusOK, `{"id":42,"login":"octocat","avatar_url":"https://example.com/avatar.png"}`), nil
				case "https://api.github.com/user/orgs":
					return authJSONResponse(http.StatusOK, `[{"login":"openai"}]`), nil
				default:
					t.Fatalf("unexpected URL: %s", req.URL.String())
					return nil, nil
				}
			}),
		})

		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://proxy.test/auth/callback?code=code-123&state=github-state", nil)

		svc.handleCallback(recorder, req)
		require.Equal(t, http.StatusForbidden, recorder.Code)
		assert.Contains(t, recorder.Body.String(), "Access Denied")
	})

	t.Run("success redirects back with issued code and success page details", func(t *testing.T) {
		t.Parallel()

		svc := newTestSimpleService(t)
		svc.cfg.SuccessPage = &SuccessPageConfig{
			Default: &SuccessPageDisplay{
				Tagline: "welcome back",
				Media: &SuccessPageMedia{
					Type: "gif",
					URL:  "https://example.com/success.gif",
				},
			},
		}
		svc.storePendingAuth("github-state", &pendingAuth{
			ClientID:      "panda",
			RedirectURI:   "http://localhost:8085/callback",
			CodeChallenge: "challenge",
			Resource:      "http://proxy.test",
			State:         "local-state",
			CreatedAt:     time.Now(),
		})
		svc.github.SetHTTPClient(&http.Client{
			Transport: authRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.String() {
				case "https://github.com/login/oauth/access_token":
					return authJSONResponse(http.StatusOK, `{"access_token":"token-123","token_type":"bearer"}`), nil
				case "https://api.github.com/user":
					return authJSONResponse(http.StatusOK, `{"id":42,"login":"octocat","avatar_url":"https://example.com/avatar.png"}`), nil
				case "https://api.github.com/user/orgs":
					return authJSONResponse(http.StatusOK, `[{"login":"ethpandaops"},{"login":"openai"}]`), nil
				default:
					t.Fatalf("unexpected URL: %s", req.URL.String())
					return nil, nil
				}
			}),
		})

		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://proxy.test/auth/callback?code=code-123&state=github-state", nil)

		svc.handleCallback(recorder, req)
		require.Equal(t, http.StatusFound, recorder.Code)

		redirectURL, err := url.Parse(recorder.Header().Get("Location"))
		require.NoError(t, err)
		query := redirectURL.Query()
		assert.Equal(t, "http://localhost:8085/callback", redirectURL.Scheme+"://"+redirectURL.Host+redirectURL.Path)
		assert.Equal(t, "local-state", query.Get("state"))
		assert.Equal(t, "octocat", query.Get("login"))
		assert.Equal(t, "https://example.com/avatar.png", query.Get("avatar_url"))
		assert.Equal(t, "ethpandaops,openai", query.Get("orgs"))
		assert.Equal(t, "welcome back", query.Get("sp_tagline"))
		assert.Equal(t, "gif", query.Get("sp_media_type"))
		assert.Equal(t, "https://example.com/success.gif", query.Get("sp_media_url"))

		code := query.Get("code")
		require.NotEmpty(t, code)

		svc.codesMu.RLock()
		issued := svc.codes[code]
		svc.codesMu.RUnlock()
		require.NotNil(t, issued)
		assert.Equal(t, "octocat", issued.GitHubLogin)
		assert.Equal(t, int64(42), issued.GitHubID)
		assert.Equal(t, []string{"ethpandaops", "openai"}, issued.Orgs)
	})
}

func TestSimpleStoreMiddlewareAndTokenHelpers(t *testing.T) {
	t.Parallel()

	t.Run("store lifecycle and cleanup helpers", func(t *testing.T) {
		t.Parallel()

		svc := newTestSimpleService(t)
		expiredAt := time.Now().Add(-2 * authCodeTTL)
		svc.storePendingAuth("fresh", &pendingAuth{CreatedAt: time.Now()})
		svc.storePendingAuth("old", &pendingAuth{CreatedAt: expiredAt})
		svc.storeIssuedCode("used", &issuedCode{CreatedAt: time.Now(), Used: true})
		svc.storeIssuedCode("expired", &issuedCode{CreatedAt: expiredAt})
		svc.cleanup()

		svc.pendingMu.RLock()
		_, freshPending := svc.pending["fresh"]
		_, oldPending := svc.pending["old"]
		svc.pendingMu.RUnlock()
		assert.True(t, freshPending)
		assert.False(t, oldPending)

		svc.codesMu.RLock()
		_, usedCode := svc.codes["used"]
		_, expiredCode := svc.codes["expired"]
		svc.codesMu.RUnlock()
		assert.False(t, usedCode)
		assert.False(t, expiredCode)

		pending, ok := svc.takePendingAuth("fresh")
		require.True(t, ok)
		require.NotNil(t, pending)
		_, ok = svc.takePendingAuth("fresh")
		assert.False(t, ok)

		close(svc.stopCh)
		svc.cleanupLoop()

		token, err := svc.generateState()
		require.NoError(t, err)
		assert.NotEmpty(t, token)

		randomToken, err := svc.generateRandomToken(8)
		require.NoError(t, err)
		assert.NotEmpty(t, randomToken)
	})

	t.Run("authorization code consumption checks", func(t *testing.T) {
		t.Parallel()

		svc := newTestSimpleService(t)
		verifier := "verifier-123"
		challenge := sha256.Sum256([]byte(verifier))
		challengeValue := base64.RawURLEncoding.EncodeToString(challenge[:])

		_, message := svc.consumeAuthorizationCode("missing", "panda", "http://localhost:8085/callback", "http://proxy.test", verifier)
		assert.Equal(t, "invalid authorization code", message)

		svc.storeIssuedCode("used", &issuedCode{
			Code:          "used",
			ClientID:      "panda",
			RedirectURI:   "http://localhost:8085/callback",
			Resource:      "http://proxy.test",
			CodeChallenge: challengeValue,
			CreatedAt:     time.Now(),
			Used:          true,
		})
		_, message = svc.consumeAuthorizationCode("used", "panda", "http://localhost:8085/callback", "http://proxy.test", verifier)
		assert.Equal(t, "authorization code already used", message)

		svc.storeIssuedCode("expired", &issuedCode{
			Code:          "expired",
			ClientID:      "panda",
			RedirectURI:   "http://localhost:8085/callback",
			Resource:      "http://proxy.test",
			CodeChallenge: challengeValue,
			CreatedAt:     time.Now().Add(-2 * authCodeTTL),
		})
		_, message = svc.consumeAuthorizationCode("expired", "panda", "http://localhost:8085/callback", "http://proxy.test", verifier)
		assert.Equal(t, "authorization code expired", message)

		svc.storeIssuedCode("mismatch", &issuedCode{
			Code:          "mismatch",
			ClientID:      "panda",
			RedirectURI:   "http://localhost:8085/callback",
			Resource:      "http://proxy.test",
			CodeChallenge: challengeValue,
			CreatedAt:     time.Now(),
		})
		_, message = svc.consumeAuthorizationCode("mismatch", "other", "http://localhost:8085/callback", "http://proxy.test", verifier)
		assert.Equal(t, "parameter mismatch", message)

		svc.storeIssuedCode("pkce", &issuedCode{
			Code:          "pkce",
			ClientID:      "panda",
			RedirectURI:   "http://localhost:8085/callback",
			Resource:      "http://proxy.test",
			CodeChallenge: challengeValue,
			CreatedAt:     time.Now(),
		})
		_, message = svc.consumeAuthorizationCode("pkce", "panda", "http://localhost:8085/callback", "http://proxy.test", "wrong-verifier")
		assert.Equal(t, "invalid code_verifier", message)

		svc.storeIssuedCode("ok", &issuedCode{
			Code:          "ok",
			ClientID:      "panda",
			RedirectURI:   "http://localhost:8085/callback",
			Resource:      "http://proxy.test",
			CodeChallenge: challengeValue,
			GitHubLogin:   "sam",
			GitHubID:      42,
			CreatedAt:     time.Now(),
		})
		issued, message := svc.consumeAuthorizationCode("ok", "panda", "http://localhost:8085/callback", "http://proxy.test", verifier)
		assert.Empty(t, message)
		require.NotNil(t, issued)
		assert.True(t, issued.Used)
		assert.Equal(t, "sam", issued.GitHubLogin)
	})

	t.Run("middleware validates bearer tokens and auth helpers", func(t *testing.T) {
		t.Parallel()

		disabled, err := NewSimpleService(logrus.New(), Config{})
		require.NoError(t, err)
		disabledRecorder := httptest.NewRecorder()
		disabledReq := httptest.NewRequest(http.MethodGet, "http://proxy.test/private", nil)
		disabled.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})).ServeHTTP(disabledRecorder, disabledReq)
		assert.Equal(t, http.StatusNoContent, disabledRecorder.Code)

		svc := newTestSimpleService(t)
		publicHandler := svc.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		protected := svc.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user := GetAuthUser(r.Context())
			require.NotNil(t, user)
			require.NoError(t, json.NewEncoder(w).Encode(user))
		}))

		publicRecorder := httptest.NewRecorder()
		publicReq := httptest.NewRequest(http.MethodGet, "http://proxy.test/health", nil)
		publicHandler.ServeHTTP(publicRecorder, publicReq)
		assert.Equal(t, http.StatusOK, publicRecorder.Code)

		missingRecorder := httptest.NewRecorder()
		missingReq := httptest.NewRequest(http.MethodGet, "http://proxy.test/private", nil)
		protected.ServeHTTP(missingRecorder, missingReq)
		assert.Equal(t, http.StatusUnauthorized, missingRecorder.Code)
		assert.Contains(t, missingRecorder.Header().Get("WWW-Authenticate"), "resource_metadata=")

		validToken, err := svc.issueAccessToken("https://proxy.test", "https://proxy.test", "sam", 42, []string{"ethpandaops"})
		require.NoError(t, err)

		validRecorder := httptest.NewRecorder()
		validReq := httptest.NewRequest(http.MethodGet, "https://proxy.test/private", nil)
		validReq.TLS = &tls.ConnectionState{}
		validReq.Header.Set("Authorization", "Bearer "+validToken)
		protected.ServeHTTP(validRecorder, validReq)
		require.Equal(t, http.StatusOK, validRecorder.Code)
		assert.Contains(t, validRecorder.Body.String(), `"GitHubLogin":"sam"`)

		invalidRecorder := httptest.NewRecorder()
		invalidReq := httptest.NewRequest(http.MethodGet, "https://proxy.test/private", nil)
		invalidReq.TLS = &tls.ConnectionState{}
		invalidReq.Header.Set("Authorization", "Bearer not-a-token")
		protected.ServeHTTP(invalidRecorder, invalidReq)
		assert.Equal(t, http.StatusUnauthorized, invalidRecorder.Code)

		refreshToken, err := svc.issueRefreshToken("https://proxy.test", "panda", "https://proxy.test", "sam", 42, []string{"ethpandaops"})
		require.NoError(t, err)

		claims, message := svc.validateRefreshToken("https://proxy.test", refreshToken, "panda", "https://proxy.test")
		assert.Empty(t, message)
		require.NotNil(t, claims)
		assert.Equal(t, "sam", claims.GitHubLogin)

		_, message = svc.validateRefreshToken("https://proxy.test", refreshToken, "other", "https://proxy.test")
		assert.Equal(t, "parameter mismatch", message)

		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(
			http.MethodPost,
			"http://proxy.test/auth/token",
			strings.NewReader("grant_type=client_credentials"),
		)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		svc.handleToken(recorder, req)
		assert.Equal(t, http.StatusBadRequest, recorder.Code)
		assert.Contains(t, recorder.Body.String(), "unsupported_grant_type")
	})
}

func newTestSimpleService(t *testing.T) *simpleService {
	t.Helper()

	service, err := NewSimpleService(logrus.New(), Config{
		Enabled: true,
		GitHub: &GitHubConfig{
			ClientID:     "github-client",
			ClientSecret: "github-secret",
		},
		Tokens: TokensConfig{SecretKey: "test-secret"},
	})
	require.NoError(t, err)

	return service.(*simpleService)
}

type authRoundTripFunc func(req *http.Request) (*http.Response, error)

func (fn authRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func authJSONResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header: http.Header{
			"Content-Type": []string{"application/json"},
		},
	}
}
