package proxy

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/internal/version"
)

type OIDCAuthenticatorConfig struct {
	IssuerURL string
	ClientID  string
}

type oidcAuthenticator struct {
	log        logrus.FieldLogger
	cfg        OIDCAuthenticatorConfig
	httpClient *http.Client

	mu       sync.RWMutex
	verifier *oidc.IDTokenVerifier
}

type oidcTokenClaims struct {
	Subject           string   `json:"sub"`
	PreferredUsername string   `json:"preferred_username"`
	Email             string   `json:"email"`
	Name              string   `json:"name"`
	Groups            []string `json:"groups"`
	Orgs              []string `json:"orgs"`
}

var _ Authenticator = (*oidcAuthenticator)(nil)

func NewOIDCAuthenticator(log logrus.FieldLogger, cfg OIDCAuthenticatorConfig) (Authenticator, error) {
	cfg.IssuerURL = strings.TrimRight(strings.TrimSpace(cfg.IssuerURL), "/")
	cfg.ClientID = strings.TrimSpace(cfg.ClientID)
	if cfg.IssuerURL == "" {
		return nil, fmt.Errorf("issuer URL is required")
	}
	if cfg.ClientID == "" {
		return nil, fmt.Errorf("client ID is required")
	}

	return &oidcAuthenticator{
		log: log.WithFields(logrus.Fields{
			"auth_mode": AuthModeOIDC,
			"issuer":    cfg.IssuerURL,
			"client_id": cfg.ClientID,
		}),
		cfg: cfg,
		httpClient: &http.Client{
			Transport: &version.Transport{},
			Timeout:   15 * time.Second,
		},
	}, nil
}

func (a *oidcAuthenticator) Start(ctx context.Context) error {
	ctx = oidc.ClientContext(ctx, a.httpClient)

	provider, err := oidc.NewProvider(ctx, a.cfg.IssuerURL)
	if err != nil {
		return fmt.Errorf("discovering OIDC provider: %w", err)
	}

	a.mu.Lock()
	a.verifier = provider.Verifier(&oidc.Config{ClientID: a.cfg.ClientID})
	a.mu.Unlock()

	a.log.Info("External OIDC authenticator initialized")

	return nil
}

func (a *oidcAuthenticator) Stop() error {
	return nil
}

func (a *oidcAuthenticator) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
			if !strings.HasPrefix(authHeader, "Bearer ") {
				writeBearerError(w, http.StatusUnauthorized, "missing or invalid Authorization header")
				return
			}

			rawToken := strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer "))
			if rawToken == "" {
				writeBearerError(w, http.StatusUnauthorized, "missing bearer token")
				return
			}

			a.mu.RLock()
			verifier := a.verifier
			a.mu.RUnlock()
			if verifier == nil {
				http.Error(w, "authenticator not initialized", http.StatusServiceUnavailable)
				return
			}

			token, err := verifier.Verify(oidc.ClientContext(r.Context(), a.httpClient), rawToken)
			if err != nil {
				a.log.WithError(err).Debug("OIDC token verification failed")
				writeBearerError(w, http.StatusUnauthorized, "invalid token")
				return
			}

			var claims oidcTokenClaims
			if err := token.Claims(&claims); err != nil {
				a.log.WithError(err).Debug("OIDC claims decoding failed")
				writeBearerError(w, http.StatusUnauthorized, "invalid token claims")
				return
			}

			subject := claims.Subject
			if subject == "" {
				subject = token.Subject
			}
			if subject == "" {
				writeBearerError(w, http.StatusUnauthorized, "token subject is missing")
				return
			}

			groups := append([]string(nil), claims.Groups...)
			if len(groups) == 0 {
				groups = append(groups, claims.Orgs...)
			}

			username := firstNonEmpty(claims.PreferredUsername, claims.Email, claims.Name, subject)

			ctx := withAuthUser(r.Context(), &AuthUser{
				Subject:  subject,
				Username: username,
				Groups:   groups,
			})

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}

	return ""
}

func writeBearerError(w http.ResponseWriter, status int, description string) {
	w.Header().Set("WWW-Authenticate",
		fmt.Sprintf(`Bearer error="invalid_token", error_description="%s"`, description))
	http.Error(w, description, status)
}
