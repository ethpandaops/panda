// Package auth provides simplified GitHub-based OAuth for local product edges.
//
// This implements a minimal OAuth 2.1 authorization server that:
// - Delegates identity verification to GitHub
// - Issues signed bearer tokens with proper resource (audience) binding per RFC 8707
// - Validates bearer tokens on protected endpoints
//
// The flow is:
// 1. Client calls /auth/authorize with resource + PKCE
// 2. Server redirects to GitHub for authentication
// 3. GitHub redirects back to /auth/callback
// 4. Server verifies org membership, issues authorization code
// 5. Client exchanges code for bearer tokens at /auth/token
// 6. Client uses bearer tokens to access product endpoints
package auth

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/auth/github"
)

const (
	// Authorization code TTL.
	authCodeTTL = 5 * time.Minute

	// Access token TTL.
	accessTokenTTL = 1 * time.Hour

	// Refresh token TTL.
	refreshTokenTTL = 30 * 24 * time.Hour

	// refreshTokenType distinguishes refresh tokens from access tokens.
	refreshTokenType = "refresh"
)

// SimpleService is the simplified auth service interface.
type SimpleService interface {
	Start(ctx context.Context) error
	Stop() error
	Enabled() bool
	Middleware() func(http.Handler) http.Handler
	MountRoutes(r chi.Router)
}

// simpleService implements SimpleService.
type simpleService struct {
	log         logrus.FieldLogger
	cfg         Config
	github      *github.Client
	secretKey   []byte
	allowedOrgs []string

	// Pending authorization requests (state -> pendingAuth).
	pending   map[string]*pendingAuth
	pendingMu sync.RWMutex

	// Authorization codes (code -> issuedCode).
	codes   map[string]*issuedCode
	codesMu sync.RWMutex

	// Lifecycle.
	stopCh chan struct{}
}

// NewSimpleService creates a new simplified auth service.
// The base URL for OAuth metadata and token issuance is derived from each
// incoming request's Host header, so no static base URL is required.
func NewSimpleService(log logrus.FieldLogger, cfg Config) (SimpleService, error) {
	log = log.WithField("component", "auth")

	if !cfg.Enabled {
		log.Info("Authentication is disabled")
		return &simpleService{log: log, cfg: cfg}, nil
	}

	if cfg.GitHub == nil {
		return nil, fmt.Errorf("github configuration is required when auth is enabled")
	}

	if cfg.Tokens.SecretKey == "" {
		return nil, fmt.Errorf("tokens.secret_key is required when auth is enabled")
	}

	s := &simpleService{
		log:         log,
		cfg:         cfg,
		github:      github.NewClient(log, cfg.GitHub.ClientID, cfg.GitHub.ClientSecret),
		secretKey:   []byte(cfg.Tokens.SecretKey),
		allowedOrgs: cfg.AllowedOrgs,
		pending:     make(map[string]*pendingAuth, 32),
		codes:       make(map[string]*issuedCode, 32),
		stopCh:      make(chan struct{}),
	}

	log.WithFields(logrus.Fields{
		"allowed_orgs": cfg.AllowedOrgs,
	}).Info("Auth service created")

	return s, nil
}

func (s *simpleService) Start(ctx context.Context) error {
	if !s.cfg.Enabled {
		return nil
	}

	// Start cleanup goroutine.
	go s.cleanupLoop()

	s.log.Info("Auth service started")
	return nil
}

func (s *simpleService) Stop() error {
	if !s.cfg.Enabled {
		return nil
	}

	close(s.stopCh)
	s.log.Info("Auth service stopped")
	return nil
}

func (s *simpleService) Enabled() bool {
	return s.cfg.Enabled
}

// MountRoutes mounts auth routes.
func (s *simpleService) MountRoutes(r chi.Router) {
	if !s.cfg.Enabled {
		return
	}

	// Discovery endpoints.
	r.Get("/.well-known/oauth-protected-resource", s.handleResourceMetadata)
	r.Get("/.well-known/oauth-authorization-server", s.handleServerMetadata)

	// OAuth endpoints.
	r.Get("/auth/authorize", s.handleAuthorize)
	r.Get("/auth/callback", s.handleCallback)
	r.Post("/auth/token", s.handleToken)
}
