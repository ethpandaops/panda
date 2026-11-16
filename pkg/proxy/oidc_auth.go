package proxy

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/golang-jwt/jwt/v5"
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

type oidcAuthenticator struct {
	log        logrus.FieldLogger
	issuers    []oidcIssuer
	httpClient *http.Client

	mu        sync.RWMutex
	verifiers map[string][]*oidc.IDTokenVerifier
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

	// Entries may share an issuer URL with different client IDs. Discover each
	// issuer once so its verifiers share one cached JWKS.
	providers := make(map[string]*oidc.Provider, len(a.issuers))
	verifiers := make(map[string][]*oidc.IDTokenVerifier, len(a.issuers))
	for _, issuer := range a.issuers {
		provider, ok := providers[issuer.issuerURL]
		if !ok {
			// NewProvider fails unless the advertised issuer equals issuerURL, so
			// it is the exact value a token's iss claim must carry.
			var err error
			provider, err = oidc.NewProvider(ctx, issuer.issuerURL)
			if err != nil {
				return fmt.Errorf("discovering OIDC provider %q: %w", issuer.issuerURL, err)
			}
			providers[issuer.issuerURL] = provider
		}

		verifiers[issuer.issuerURL] = append(verifiers[issuer.issuerURL],
			provider.Verifier(&oidc.Config{ClientID: issuer.clientID}))
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

			// Route by the unverified iss claim, then fully verify against that
			// issuer only. go-oidc checks the signature before the issuer and
			// refetches JWKS on every unknown kid, so trying each issuer in turn
			// costs a JWKS fetch from the wrong IdP per request.
			var unverified jwt.RegisteredClaims
			if _, _, err := jwt.NewParser().ParseUnverified(rawToken, &unverified); err != nil {
				writeBearerError(w, http.StatusUnauthorized, "malformed token")
				return
			}

			issuerVerifiers, ok := verifiers[unverified.Issuer]
			if !ok {
				a.log.WithField("iss", unverified.Issuer).Debug("OIDC token from untrusted issuer")
				writeBearerError(w, http.StatusUnauthorized, "untrusted token issuer")
				return
			}

			// Same-issuer verifiers share one key set, so trying each client ID
			// costs no extra JWKS fetch.
			verifyCtx := oidc.ClientContext(r.Context(), a.httpClient)
			var token *oidc.IDToken
			var verifyErr error
			for _, verifier := range issuerVerifiers {
				if token, verifyErr = verifier.Verify(verifyCtx, rawToken); verifyErr == nil {
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
