package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// errRefreshTokenAlreadyConsumed is returned by rotateRefreshToken when the
// presented refresh token has already been rotated by a concurrent request.
var errRefreshTokenAlreadyConsumed = errors.New("refresh token already consumed")

func (s *authorizationServer) issueAccessToken(
	issuerURL, resource, githubLogin string, githubID int64, orgs []string,
) (string, error) {
	now := time.Now()
	claims := &tokenClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    issuerURL,
			Subject:   fmt.Sprintf("%d", githubID),
			Audience:  jwt.ClaimStrings{resource},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(s.accessTokenTTL)),
		},
		GitHubLogin: githubLogin,
		GitHubID:    githubID,
		Orgs:        orgs,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(s.secretKey)
}

func (s *authorizationServer) writeTokenResponse(w http.ResponseWriter, accessToken, refreshToken string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")

	response := map[string]any{
		"access_token": accessToken,
		"token_type":   "Bearer",
		"expires_in":   int(s.accessTokenTTL.Seconds()),
	}
	if refreshToken != "" {
		response["refresh_token"] = refreshToken
	}

	_ = json.NewEncoder(w).Encode(response)
}

func (s *authorizationServer) issueRefreshToken(
	clientID, resource, githubLogin string, githubID int64, githubToken string, orgs []string,
) (string, error) {
	if githubToken == "" {
		return "", fmt.Errorf("missing GitHub access token")
	}

	refreshToken, err := s.generateRandomToken(32)
	if err != nil {
		return "", err
	}

	s.refreshSessionsMu.Lock()
	s.refreshSessions[refreshToken] = &refreshSession{
		ClientID:          clientID,
		Resource:          resource,
		GitHubLogin:       githubLogin,
		GitHubID:          githubID,
		GitHubAccessToken: githubToken,
		Orgs:              append([]string(nil), orgs...),
		CreatedAt:         time.Now(),
		ExpiresAt:         time.Now().Add(s.refreshTokenTTL),
	}
	s.refreshSessionsMu.Unlock()

	return refreshToken, nil
}

// rotateRefreshToken atomically consumes currentRefreshToken and replaces it
// with a newly issued one. The presence check and the delete-then-insert
// happen under a single lock, so a currentRefreshToken already consumed by a
// concurrent request (returning errRefreshTokenAlreadyConsumed here) can never
// be rotated twice into two independently live token families.
func (s *authorizationServer) rotateRefreshToken(
	currentRefreshToken, clientID, resource, githubLogin string,
	githubID int64,
	githubToken string,
	orgs []string,
) (string, error) {
	newRefreshToken, err := s.generateRandomToken(32)
	if err != nil {
		return "", err
	}

	s.refreshSessionsMu.Lock()
	defer s.refreshSessionsMu.Unlock()

	if _, stillPresent := s.refreshSessions[currentRefreshToken]; !stillPresent {
		return "", errRefreshTokenAlreadyConsumed
	}

	delete(s.refreshSessions, currentRefreshToken)
	s.refreshSessions[newRefreshToken] = &refreshSession{
		ClientID:          clientID,
		Resource:          resource,
		GitHubLogin:       githubLogin,
		GitHubID:          githubID,
		GitHubAccessToken: githubToken,
		Orgs:              append([]string(nil), orgs...),
		CreatedAt:         time.Now(),
		ExpiresAt:         time.Now().Add(s.refreshTokenTTL),
	}

	return newRefreshToken, nil
}

func (s *authorizationServer) verifyPKCE(verifier, challenge string) bool {
	hash := sha256.Sum256([]byte(verifier))
	computed := base64.RawURLEncoding.EncodeToString(hash[:])
	return computed == challenge
}

func (s *authorizationServer) generateState() (string, error) {
	return s.generateRandomToken(32)
}

func (s *authorizationServer) generateRandomToken(size int) (string, error) {
	bytes := make([]byte, size)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}

	return base64.RawURLEncoding.EncodeToString(bytes), nil
}
