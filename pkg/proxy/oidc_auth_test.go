package proxy

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/sirupsen/logrus"
)

func TestOIDCAuthenticatorMiddlewareAcceptsValidToken(t *testing.T) {
	t.Parallel()

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}

	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":   server.URL,
			"jwks_uri": server.URL + "/keys",
		})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{
				{
					"kty": "RSA",
					"kid": "test-key",
					"alg": "RS256",
					"use": "sig",
					"n":   base64.RawURLEncoding.EncodeToString(privateKey.N.Bytes()),
					"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(privateKey.E)).Bytes()),
				},
			},
		})
	})

	authenticator, err := NewOIDCAuthenticator(logrus.New(), OIDCAuthenticatorConfig{
		IssuerURL: server.URL,
		ClientID:  "panda-proxy",
	})
	if err != nil {
		t.Fatalf("NewOIDCAuthenticator failed: %v", err)
	}

	if err := authenticator.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	rawToken := signedRSAToken(t, privateKey, server.URL, "panda-proxy", "user-123")
	handler := authenticator.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := GetAuthUser(r.Context())
		if user == nil {
			t.Fatal("expected auth user in context")
		}
		if user.Subject != "user-123" {
			t.Fatalf("expected subject user-123, got %q", user.Subject)
		}
		if user.Username != "sam" {
			t.Fatalf("expected username sam, got %q", user.Username)
		}
		if len(user.Groups) != 1 || user.Groups[0] != "ethpandaops" {
			t.Fatalf("unexpected groups: %#v", user.Groups)
		}

		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/clickhouse/query", nil)
	req.Header.Set("Authorization", "Bearer "+rawToken)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d: %s", http.StatusNoContent, rec.Code, rec.Body.String())
	}
}

func TestOIDCAuthenticatorRejectsAudienceMismatch(t *testing.T) {
	t.Parallel()

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}

	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":   server.URL,
			"jwks_uri": server.URL + "/keys",
		})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{
				{
					"kty": "RSA",
					"kid": "test-key",
					"alg": "RS256",
					"use": "sig",
					"n":   base64.RawURLEncoding.EncodeToString(privateKey.N.Bytes()),
					"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(privateKey.E)).Bytes()),
				},
			},
		})
	})

	authenticator, err := NewOIDCAuthenticator(logrus.New(), OIDCAuthenticatorConfig{
		IssuerURL: server.URL,
		ClientID:  "panda-proxy",
	})
	if err != nil {
		t.Fatalf("NewOIDCAuthenticator failed: %v", err)
	}

	if err := authenticator.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	rawToken := signedRSAToken(t, privateKey, server.URL, "wrong-audience", "user-123")
	handler := authenticator.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/clickhouse/query", nil)
	req.Header.Set("Authorization", "Bearer "+rawToken)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func signedRSAToken(t *testing.T, privateKey *rsa.PrivateKey, issuer, audience, subject string) string {
	t.Helper()

	now := time.Now()
	claims := jwt.MapClaims{
		"iss":                issuer,
		"aud":                []string{audience},
		"sub":                subject,
		"preferred_username": "sam",
		"groups":             []string{"ethpandaops"},
		"iat":                now.Unix(),
		"exp":                now.Add(time.Hour).Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = "test-key"

	signed, err := token.SignedString(privateKey)
	if err != nil {
		t.Fatalf("SignedString failed: %v", err)
	}

	return signed
}
