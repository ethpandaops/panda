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

// OIDCIssuerConfig identifies a trusted OIDC issuer and the audience (client ID)
// expected in the tokens it issues.
type OIDCIssuerConfig struct {
	IssuerURL string `yaml:"issuer_url"`
	ClientID  string `yaml:"client_id"`
}

// OIDCAuthenticatorConfig configures the external OIDC authenticator. A token is
// accepted if it verifies against ANY configured issuer, which lets the proxy
// trust (for example) humans on one IdP and machine identities on another
// simultaneously. At least one issuer is required.
type OIDCAuthenticatorConfig struct {
	Issuers []OIDCIssuerConfig
}

type oidcIssuer struct {
	issuerURL string
	clientID  string
}

type oidcVerifier struct {
	issuerURL string
	verifier  *oidc.IDTokenVerifier
}

type oidcAuthenticator struct {
	log        logrus.FieldLogger
	issuers    []oidcIssuer
	httpClient *http.Client

	mu        sync.RWMutex
	verifiers []oidcVerifier
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
	if len(cfg.Issuers) == 0 {
		return nil, fmt.Errorf("at least one issuer is required")
	}

	issuers := make([]oidcIssuer, 0, len(cfg.Issuers))
	for i, candidate := range cfg.Issuers {
		// Pass the issuer through as-is (no trailing-slash trimming): go-oidc
		// requires the configured issuer to match the IdP's advertised issuer
		// exactly, and some IdPs advertise a path with a trailing slash (e.g.
		// Authentik's ".../application/o/<app>/").
		issuerURL := strings.TrimSpace(candidate.IssuerURL)
		clientID := strings.TrimSpace(candidate.ClientID)
		if issuerURL == "" {
			return nil, fmt.Errorf("issuer URL is required (issuer index %d)", i)
		}
		if clientID == "" {
			return nil, fmt.Errorf("client ID is required for issuer %q", issuerURL)
		}
		issuers = append(issuers, oidcIssuer{issuerURL: issuerURL, clientID: clientID})
	}

	return &oidcAuthenticator{
		log: log.WithFields(logrus.Fields{
			"auth_mode": AuthModeOIDC,
			"issuers":   issuerURLs(issuers),
		}),
		issuers: issuers,
		httpClient: &http.Client{
			Transport: &version.Transport{},
			Timeout:   15 * time.Second,
		},
	}, nil
}

func (a *oidcAuthenticator) Start(ctx context.Context) error {
	ctx = oidc.ClientContext(ctx, a.httpClient)

	verifiers := make([]oidcVerifier, 0, len(a.issuers))
	for _, issuer := range a.issuers {
		provider, err := oidc.NewProvider(ctx, issuer.issuerURL)
		if err != nil {
			return fmt.Errorf("discovering OIDC provider %q: %w", issuer.issuerURL, err)
		}

		verifiers = append(verifiers, oidcVerifier{
			issuerURL: issuer.issuerURL,
			verifier:  provider.Verifier(&oidc.Config{ClientID: issuer.clientID}),
		})
	}

	a.mu.Lock()
	a.verifiers = verifiers
	a.mu.Unlock()

	a.log.WithField("issuer_count", len(verifiers)).Info("External OIDC authenticator initialized")

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
			verifiers := a.verifiers
			a.mu.RUnlock()
			if len(verifiers) == 0 {
				http.Error(w, "authenticator not initialized", http.StatusServiceUnavailable)
				return
			}

			// Accept the token if it verifies against any trusted issuer. Each
			// verifier checks issuer, signature, and audience, so a token only
			// passes for the issuer that actually minted it.
			verifyCtx := oidc.ClientContext(r.Context(), a.httpClient)
			var token *oidc.IDToken
			var verifyErr error
			for _, v := range verifiers {
				token, verifyErr = v.verifier.Verify(verifyCtx, rawToken)
				if verifyErr == nil {
					break
				}
			}
			if verifyErr != nil {
				a.log.WithError(verifyErr).Debug("OIDC token verification failed")
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

func issuerURLs(issuers []oidcIssuer) []string {
	urls := make([]string, 0, len(issuers))
	for _, issuer := range issuers {
		urls = append(urls, issuer.issuerURL)
	}

	return urls
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
