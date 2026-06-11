// Package attribution carries free-text caller attribution (who a request
// acts on behalf of) across panda's hops: CLI -> server -> proxy. Values
// are untrusted and audit-only — they must never influence authorization.
package attribution

import (
	"context"
	"strings"
)

// Header is the HTTP header that carries attribution between panda
// components.
const Header = "X-Panda-On-Behalf-Of"

// EnvVar is the environment variable callers (e.g. chat agents acting for
// a human user) set per invocation to attribute their requests.
const EnvVar = "PANDA_ON_BEHALF_OF"

type contextKey struct{}

// WithValue returns a context carrying the attribution value. Empty or
// whitespace-only values leave the context unchanged.
func WithValue(ctx context.Context, value string) context.Context {
	value = strings.TrimSpace(value)
	if value == "" {
		return ctx
	}

	return context.WithValue(ctx, contextKey{}, value)
}

// FromContext returns the attribution value carried by the context, or an
// empty string when none is set.
func FromContext(ctx context.Context) string {
	value, _ := ctx.Value(contextKey{}).(string)
	return value
}
