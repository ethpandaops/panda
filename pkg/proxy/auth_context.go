package proxy

import "context"

// AuthUser represents the authenticated identity for proxy requests.
type AuthUser struct {
	Subject  string
	Username string
	Groups   []string
}

type authUserContextKey string

const proxyAuthUserKey authUserContextKey = "proxy_auth_user"

func withAuthUser(ctx context.Context, user *AuthUser) context.Context {
	return context.WithValue(ctx, proxyAuthUserKey, user)
}

// GetAuthUser returns the authenticated proxy user from the request context.
func GetAuthUser(ctx context.Context) *AuthUser {
	user, _ := ctx.Value(proxyAuthUserKey).(*AuthUser)
	return user
}
