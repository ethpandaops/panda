package proxyserver

import (
	"context"
	"net/http"
	"strconv"

	"github.com/sirupsen/logrus"

	simpleauth "github.com/ethpandaops/panda/pkg/auth"
)

// AuthMode determines how the proxy authenticates requests.
type AuthMode string

const (
	// AuthModeNone disables authentication (for local development only).
	AuthModeNone AuthMode = "none"

	// AuthModeOAuth enables GitHub-backed OAuth on the proxy control plane and
	// validates proxy-issued bearer tokens on data-plane routes.
	AuthModeOAuth AuthMode = "oauth"
)

// Authenticator validates incoming requests to the proxy.
type Authenticator interface {
	// Middleware returns an HTTP middleware that authenticates requests.
	Middleware() func(http.Handler) http.Handler

	// Start starts any background processes.
	Start(ctx context.Context) error

	// Stop stops any background processes.
	Stop() error
}

// noneAuthenticator allows all requests without authentication.
// This is for local development only.
type noneAuthenticator struct {
	log logrus.FieldLogger
}

// Compile-time interface checks.
var (
	_ Authenticator = (*noneAuthenticator)(nil)
	_ Authenticator = (*simpleServiceAuthenticator)(nil)
)

// NewNoneAuthenticator creates an authenticator that allows all requests.
func NewNoneAuthenticator(log logrus.FieldLogger) Authenticator {
	return &noneAuthenticator{
		log: log.WithField("auth_mode", AuthModeNone),
	}
}

func (a *noneAuthenticator) Start(_ context.Context) error {
	a.log.Warn("Authentication is DISABLED - this should only be used for local development")

	return nil
}

func (a *noneAuthenticator) Stop() error {
	return nil
}

func (a *noneAuthenticator) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r)
		})
	}
}

type simpleServiceAuthenticator struct {
	svc simpleauth.SimpleService
}

func NewSimpleServiceAuthenticator(svc simpleauth.SimpleService) Authenticator {
	return &simpleServiceAuthenticator{svc: svc}
}

func (a *simpleServiceAuthenticator) Start(ctx context.Context) error {
	return a.svc.Start(ctx)
}

func (a *simpleServiceAuthenticator) Stop() error {
	return a.svc.Stop()
}

func (a *simpleServiceAuthenticator) Middleware() func(http.Handler) http.Handler {
	return a.svc.Middleware()
}

// GetUserID extracts the authenticated user ID from the request context.
func GetUserID(ctx context.Context) string {
	user := simpleauth.GetAuthUser(ctx)
	if user == nil || user.GitHubID == 0 {
		return ""
	}

	return strconv.FormatInt(user.GitHubID, 10)
}
