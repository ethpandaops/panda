// Package version provides build version information.
package version

import "net/http"

// These variables are set at build time via ldflags.
var (
	Version   = "dev"
	GitCommit = "unknown"
	BuildTime = "unknown"
)

// UserAgent returns the User-Agent string for outbound HTTP requests.
func UserAgent() string {
	return userAgent
}

// userAgent is computed once at init since Version is set via ldflags and
// never changes at runtime.
var userAgent = "panda/" + Version

// Transport wraps an http.RoundTripper to inject the panda User-Agent header.
type Transport struct {
	Base http.RoundTripper
}

// RoundTrip implements http.RoundTripper.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Shallow-copy the request and clone only the header map to avoid the
	// cost of a full req.Clone while still satisfying the RoundTripper
	// contract (do not mutate the original request).
	r := *req
	r.Header = req.Header.Clone()
	r.Header.Set("User-Agent", userAgent)

	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}

	return base.RoundTrip(&r)
}
