// Package token provides grant-agnostic access-token sources for proxy auth.
//
// A Source hides which OAuth grant is in play (interactive refresh-token vs
// client_credentials) behind a single Token/Invalidate seam, so request code
// can attach a token and retry on rejection without branching on auth mode.
package token

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/auth/client"
	"github.com/ethpandaops/panda/pkg/auth/store"
)

// ModeClientCredentials selects the non-interactive service-account grant. Any
// other mode ("", "oauth", "oidc") uses the interactive refresh-token grant.
const ModeClientCredentials = "client_credentials"

// clientCredentialsBuffer is how long before expiry a cached client_credentials
// access token is re-minted, in addition to the proactive fractional refresh.
const clientCredentialsBuffer = 5 * time.Minute

// Config describes how to build a Source. A blank IssuerURL or ClientID means
// no auth is configured and NewSource returns nil.
type Config struct {
	// IssuerURL is the resolved OIDC issuer (callers apply any proxy-URL
	// fallback before passing it in).
	IssuerURL string
	// ClientID is the OAuth client ID.
	ClientID string
	// Resource is the optional RFC 8707 resource indicator.
	Resource string
	// Username and Password are the service-account credentials for
	// ModeClientCredentials.
	Username string
	Password string
	// Mode selects the grant: ModeClientCredentials or interactive (default).
	Mode string
	// RefreshTokenTTL is the expected refresh-token lifetime, used by the
	// interactive store to keep the refresh token alive via rotation.
	RefreshTokenTTL time.Duration
	// MintTimeout bounds a single client_credentials mint.
	MintTimeout time.Duration
}

// Source yields valid access tokens for proxy requests, hiding the OAuth grant.
type Source interface {
	// Token returns a currently valid access token, refreshing or minting one
	// as needed.
	Token(ctx context.Context) (string, error)

	// Invalidate drops any cached access token so the next Token call obtains a
	// fresh one. Used when the proxy rejects a token that has not yet hit the
	// local expiry buffer (e.g. server-side revocation).
	Invalidate()
}

// refreshSource is the interactive (authorization-code / device) grant. It
// delegates to the on-disk credential store, which refreshes via the refresh
// token and persists rotation.
type refreshSource struct {
	store store.Store
}

// clientCredentialsSource is the non-interactive service-account grant. It
// mints access tokens on demand and caches them in memory only — no refresh
// token, nothing written to disk.
type clientCredentialsSource struct {
	log         logrus.FieldLogger
	client      client.Client
	mintTimeout time.Duration

	mu     sync.Mutex
	tokens *client.Tokens
}

// NewSource builds the access-token Source for cfg, owning the grant decision
// and the construction of the OAuth client and (for interactive grants) the
// on-disk credential store. It returns nil when no auth is configured, so the
// proxy can treat "no token source" as "no auth required".
func NewSource(log logrus.FieldLogger, cfg Config) Source {
	issuer := strings.TrimRight(cfg.IssuerURL, "/")
	clientID := strings.TrimSpace(cfg.ClientID)

	if issuer == "" || clientID == "" {
		return nil
	}

	resource := strings.TrimRight(cfg.Resource, "/")

	authClient := client.New(log, client.Config{
		IssuerURL: issuer,
		ClientID:  clientID,
		Resource:  resource,
		Username:  cfg.Username,
		Password:  cfg.Password,
	})

	if cfg.Mode == ModeClientCredentials {
		return NewClientCredentialsSource(log, authClient, cfg.MintTimeout)
	}

	return NewRefreshSource(store.New(log, store.Config{
		AuthClient:      authClient,
		IssuerURL:       issuer,
		ClientID:        clientID,
		Resource:        resource,
		RefreshTokenTTL: cfg.RefreshTokenTTL,
	}))
}

// NewRefreshSource builds a Source backed by the on-disk credential store.
func NewRefreshSource(s store.Store) Source {
	return &refreshSource{store: s}
}

// NewClientCredentialsSource builds a Source that mints tokens via the
// client_credentials grant, caching them in memory for mintTimeout-bounded
// re-mints.
func NewClientCredentialsSource(log logrus.FieldLogger, c client.Client, mintTimeout time.Duration) Source {
	return &clientCredentialsSource{
		log:         log.WithField("component", "client-credentials-token"),
		client:      c,
		mintTimeout: mintTimeout,
	}
}

// Token returns a valid access token from the credential store.
func (s *refreshSource) Token(_ context.Context) (string, error) {
	return s.store.GetAccessToken()
}

// Invalidate forces the store to refresh on the next Token call.
func (s *refreshSource) Invalidate() {
	s.store.Invalidate()
}

// Token returns the cached client_credentials access token, re-minting via the
// issuer's token endpoint when missing or past the refresh threshold.
func (s *clientCredentialsSource) Token(_ context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.tokens != nil && !client.ShouldRefresh(
		time.Now(), s.tokens.ExpiresAt, s.tokens.ExpiresIn,
		clientCredentialsBuffer, client.DefaultRefreshFraction,
	) {
		return s.tokens.AccessToken, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.mintTimeout)
	defer cancel()

	tokens, err := s.client.ClientCredentials(ctx)
	if err != nil {
		// Keep serving a still-valid cached token across transient mint failures.
		if s.tokens != nil && time.Now().Before(s.tokens.ExpiresAt) {
			s.log.WithError(err).Warn("Re-minting client_credentials token failed; using cached token")

			return s.tokens.AccessToken, nil
		}

		return "", fmt.Errorf("minting client_credentials token: %w", err)
	}

	s.tokens = tokens

	return tokens.AccessToken, nil
}

// Invalidate drops the cached client_credentials token so the next Token call
// mints a fresh one.
func (s *clientCredentialsSource) Invalidate() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.tokens = nil
}
