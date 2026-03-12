// Package version provides build version information.
package version

import "net/http"

// These variables are set at build time via ldflags.
var (
	// Version is the semantic version of the build.
	Version = "dev"

	// GitCommit is the git commit hash of the build.
	GitCommit = "unknown"

	// BuildTime is the time the build was created.
	BuildTime = "unknown"
)

// UserAgent returns the User-Agent string for outbound HTTP requests.
func UserAgent() string {
	return "panda/" + Version
}

// Transport wraps an http.RoundTripper to inject the panda User-Agent header.
type Transport struct {
	Base http.RoundTripper
}

// RoundTrip implements http.RoundTripper.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.Header.Set("User-Agent", UserAgent())

	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}

	return base.RoundTrip(r)
}

// NewHTTPClient returns an http.Client that sets the panda User-Agent on
// every outbound request.
func NewHTTPClient() *http.Client {
	return &http.Client{
		Transport: &Transport{},
	}
}
