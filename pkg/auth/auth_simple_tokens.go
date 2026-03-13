package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// refreshTokenClaims are the JWT claims for stateless refresh tokens.
type refreshTokenClaims struct {
	jwt.RegisteredClaims
	GitHubLogin string   `json:"github_login"`
	GitHubID    int64    `json:"github_id"`
	Orgs        []string `json:"orgs,omitempty"`
	ClientID    string   `json:"client_id"`
	Resource    string   `json:"resource"`
	TokenType   string   `json:"token_type"`
}

// tokenClaims are the JWT claims for access tokens.
type tokenClaims struct {
	jwt.RegisteredClaims
	GitHubLogin string   `json:"github_login"`
	GitHubID    int64    `json:"github_id"`
	Orgs        []string `json:"orgs,omitempty"`
}

// Middleware returns bearer-token validation middleware.
func (s *simpleService) Middleware() func(http.Handler) http.Handler {
	if !s.cfg.Enabled {
		return func(next http.Handler) http.Handler { return next }
	}

	publicPaths := map[string]bool{
		"/":                                     true,
		"/health":                               true,
		"/ready":                                true,
		"/.well-known/oauth-protected-resource": true,
		"/.well-known/oauth-authorization-server": true,
	}

	publicPrefixes := []string{"/auth/", "/.well-known/"}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if publicPaths[r.URL.Path] {
				next.ServeHTTP(w, r)
				return
			}

			for _, prefix := range publicPrefixes {
				if strings.HasPrefix(r.URL.Path, prefix) {
					next.ServeHTTP(w, r)
					return
				}
			}

			authHeader := r.Header.Get("Authorization")
			if !strings.HasPrefix(authHeader, "Bearer ") {
				s.writeUnauthorized(w, baseURLFromRequest(r), "missing or invalid Authorization header")
				return
			}

			baseURL := baseURLFromRequest(r)
			claims, err := s.validateAccessToken(baseURL, strings.TrimPrefix(authHeader, "Bearer "))
			if err != nil {
				s.writeUnauthorized(w, baseURL, err.Error())
				return
			}

			ctx := context.WithValue(r.Context(), authUserKey, &AuthUser{
				GitHubLogin: claims.GitHubLogin,
				GitHubID:    claims.GitHubID,
				Orgs:        claims.Orgs,
			})

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func (s *simpleService) validateAccessToken(issuerURL, tokenStr string) (*tokenClaims, error) {
	if tokenStr == "" {
		return nil, fmt.Errorf("missing or invalid Authorization header")
	}

	claims := &tokenClaims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (any, error) {
		if t.Method.Alg() != jwt.SigningMethodHS256.Alg() {
			return nil, fmt.Errorf("unexpected signing method")
		}

		return s.secretKey, nil
	}, jwt.WithIssuer(issuerURL), jwt.WithExpirationRequired())
	if err != nil || !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}

	for _, aud := range claims.Audience {
		if aud == issuerURL {
			return claims, nil
		}
	}

	return nil, fmt.Errorf("token audience mismatch")
}

func (s *simpleService) validateRefreshToken(
	issuerURL, refreshToken, clientID, resource string,
) (*refreshTokenClaims, string) {
	claims := &refreshTokenClaims{}
	token, err := jwt.ParseWithClaims(refreshToken, claims, func(t *jwt.Token) (any, error) {
		if t.Method.Alg() != jwt.SigningMethodHS256.Alg() {
			return nil, fmt.Errorf("unexpected signing method")
		}

		return s.secretKey, nil
	}, jwt.WithIssuer(issuerURL), jwt.WithExpirationRequired())
	if err != nil || !token.Valid {
		return nil, "invalid refresh token"
	}

	if claims.TokenType != refreshTokenType {
		return nil, "invalid refresh token"
	}

	if claims.ClientID != clientID {
		return nil, "parameter mismatch"
	}

	if resource != "" && claims.Resource != resource {
		return nil, "parameter mismatch"
	}

	return claims, ""
}

// AuthUser is the authenticated user info attached to request context.
type AuthUser struct {
	GitHubLogin string
	GitHubID    int64
	Orgs        []string
}

// GetAuthUser returns the authenticated user from context.
func GetAuthUser(ctx context.Context) *AuthUser {
	user, _ := ctx.Value(authUserKey).(*AuthUser)
	return user
}

type authUserKeyType string

const authUserKey authUserKeyType = "auth_user"

func (s *simpleService) issueAccessToken(
	issuerURL, resource, githubLogin string, githubID int64, orgs []string,
) (string, error) {
	now := time.Now()
	claims := &tokenClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    issuerURL,
			Subject:   fmt.Sprintf("%d", githubID),
			Audience:  jwt.ClaimStrings{resource},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(accessTokenTTL)),
		},
		GitHubLogin: githubLogin,
		GitHubID:    githubID,
		Orgs:        orgs,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(s.secretKey)
}

func (s *simpleService) issueRefreshToken(
	issuerURL, clientID, resource, githubLogin string,
	githubID int64,
	orgs []string,
) (string, error) {
	now := time.Now()
	claims := &refreshTokenClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    issuerURL,
			Subject:   fmt.Sprintf("%d", githubID),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(refreshTokenTTL)),
		},
		GitHubLogin: githubLogin,
		GitHubID:    githubID,
		Orgs:        append([]string(nil), orgs...),
		ClientID:    clientID,
		Resource:    resource,
		TokenType:   refreshTokenType,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(s.secretKey)
}

func (s *simpleService) writeTokenResponse(w http.ResponseWriter, accessToken, refreshToken string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")

	response := map[string]any{
		"access_token": accessToken,
		"token_type":   "Bearer",
		"expires_in":   int(accessTokenTTL.Seconds()),
	}
	if refreshToken != "" {
		response["refresh_token"] = refreshToken
	}

	_ = json.NewEncoder(w).Encode(response)
}

func (s *simpleService) verifyPKCE(verifier, challenge string) bool {
	hash := sha256.Sum256([]byte(verifier))
	computed := base64.RawURLEncoding.EncodeToString(hash[:])

	return computed == challenge
}

func (s *simpleService) writeUnauthorized(w http.ResponseWriter, baseURL, description string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("WWW-Authenticate", fmt.Sprintf(
		`Bearer resource_metadata="%s/.well-known/oauth-protected-resource", error="invalid_token", error_description="%s"`,
		baseURL, description))
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":             "invalid_token",
		"error_description": description,
	})
}
