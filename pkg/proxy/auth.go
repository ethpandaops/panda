package proxy

import (
	"context"
	"net/http"
	"strings"

	"github.com/sirupsen/logrus"
)

// AuthMode determines how the proxy authenticates requests.
type AuthMode string

const (
	// AuthModeNone disables authentication (for local development only).
	AuthModeNone AuthMode = "none"

	// AuthModeToken uses local per-execution tokens for trusted runtime hops.
	AuthModeToken AuthMode = "token"

	// AuthModeJWT uses JWTs validated against a remote JWKS (for standalone K8s deployment).
	AuthModeJWT AuthMode = "jwt"
)

// Authenticator validates incoming requests to the proxy.
type Authenticator interface {
	// Middleware returns an HTTP middleware that authenticates requests.
	Middleware() func(http.Handler) http.Handler

	// Start starts any background processes (e.g., JWKS refresh).
	Start(ctx context.Context) error

	// Stop stops any background processes.
	Stop() error
}

// noneAuthenticator allows all requests without authentication.
// This is for local development only.
type noneAuthenticator struct {
	log logrus.FieldLogger
}

// Compile-time interface check.
var _ Authenticator = (*noneAuthenticator)(nil)

// NewNoneAuthenticator creates an authenticator that allows all requests.
func NewNoneAuthenticator(log logrus.FieldLogger) Authenticator {
	return &noneAuthenticator{
		log: log.WithField("auth_mode", "none"),
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
			// No authentication - allow all requests.
			ctx := context.WithValue(r.Context(), userIDKey, "anonymous")
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// tokenAuthenticator uses local per-execution tokens.
type tokenAuthenticator struct {
	log    logrus.FieldLogger
	tokens *TokenStore
}

// Compile-time interface check.
var _ Authenticator = (*tokenAuthenticator)(nil)

// NewTokenAuthenticator creates an authenticator using local tokens.
func NewTokenAuthenticator(log logrus.FieldLogger, tokens *TokenStore) Authenticator {
	return &tokenAuthenticator{
		log:    log.WithField("auth_mode", "token"),
		tokens: tokens,
	}
}

func (a *tokenAuthenticator) Start(_ context.Context) error {
	return nil
}

func (a *tokenAuthenticator) Stop() error {
	return nil
}

func (a *tokenAuthenticator) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Extract token from Authorization header.
			auth := r.Header.Get("Authorization")
			if auth == "" {
				http.Error(w, "missing Authorization header", http.StatusUnauthorized)

				return
			}

			// Expect "Bearer <token>" format.
			if !strings.HasPrefix(auth, "Bearer ") {
				http.Error(w, "invalid Authorization header format", http.StatusUnauthorized)

				return
			}

			token := strings.TrimPrefix(auth, "Bearer ")

			// Validate token.
			executionID := a.tokens.Validate(token)
			if executionID == "" {
				http.Error(w, "invalid or expired token", http.StatusUnauthorized)

				return
			}

			// Add execution ID to request context.
			ctx := context.WithValue(r.Context(), executionIDKey, executionID)
			ctx = context.WithValue(ctx, userIDKey, executionID) // Use execution ID as user ID for consistency

			a.log.WithFields(logrus.Fields{
				"execution_id": executionID,
				"path":         r.URL.Path,
				"method":       r.Method,
			}).Debug("Authenticated request (token)")

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// jwtAuthenticator uses JWTs validated against a remote JWKS.
type jwtAuthenticator struct {
	log       logrus.FieldLogger
	validator JWTValidator
}

// Compile-time interface check.
var _ Authenticator = (*jwtAuthenticator)(nil)

// NewJWTAuthenticator creates an authenticator using JWT validation.
func NewJWTAuthenticator(log logrus.FieldLogger, validator JWTValidator) Authenticator {
	return &jwtAuthenticator{
		log:       log.WithField("auth_mode", "jwt"),
		validator: validator,
	}
}

func (a *jwtAuthenticator) Start(ctx context.Context) error {
	return a.validator.Start(ctx)
}

func (a *jwtAuthenticator) Stop() error {
	return a.validator.Stop()
}

func (a *jwtAuthenticator) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Extract JWT from Authorization header.
			auth := r.Header.Get("Authorization")
			if auth == "" {
				http.Error(w, "missing Authorization header", http.StatusUnauthorized)

				return
			}

			// Expect "Bearer <jwt>" format.
			if !strings.HasPrefix(auth, "Bearer ") {
				http.Error(w, "invalid Authorization header format", http.StatusUnauthorized)

				return
			}

			tokenString := strings.TrimPrefix(auth, "Bearer ")

			// Validate JWT.
			claims, err := a.validator.Validate(r.Context(), tokenString)
			if err != nil {
				a.log.WithError(err).Debug("JWT validation failed")
				http.Error(w, "invalid or expired token", http.StatusUnauthorized)

				return
			}

			// Add claims to request context.
			ctx := context.WithValue(r.Context(), userIDKey, claims.Subject)
			ctx = context.WithValue(ctx, jwtClaimsKey, claims)

			a.log.WithFields(logrus.Fields{
				"user_id": claims.Subject,
				"email":   claims.Email,
				"path":    r.URL.Path,
				"method":  r.Method,
			}).Debug("Authenticated request (jwt)")

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// contextKey is the type for context keys used in the proxy package.
type contextKey string

// Context keys for authenticated request data.
const (
	executionIDKey contextKey = "execution_id"
	userIDKey      contextKey = "user_id"
	jwtClaimsKey   contextKey = "jwt_claims"
)

// GetUserID extracts the user ID from the request context.
func GetUserID(ctx context.Context) string {
	if v := ctx.Value(userIDKey); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}

	return ""
}

// GetExecutionID extracts the execution ID from the request context.
func GetExecutionID(ctx context.Context) string {
	if v := ctx.Value(executionIDKey); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}

	return ""
}

// GetJWTClaims extracts the JWT claims from the request context.
func GetJWTClaims(ctx context.Context) *JWTClaims {
	if v := ctx.Value(jwtClaimsKey); v != nil {
		if c, ok := v.(*JWTClaims); ok {
			return c
		}
	}

	return nil
}
