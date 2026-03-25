// Package githubapi provides shared helpers for authenticated GitHub API
// requests. Both the EIP and consensus-specs fetchers use these helpers
// to build requests with optional bearer-token auth from GITHUB_TOKEN or
// GH_TOKEN environment variables.
package githubapi

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// APIBase is the root URL for the GitHub REST API.
const APIBase = "https://api.github.com"

// getenv is package-level so tests can override it.
var getenv = os.Getenv

// NewRequest creates an HTTP GET request for the GitHub API.
// If GITHUB_TOKEN or GH_TOKEN is set, a Bearer authorization header is
// added automatically.
func NewRequest(ctx context.Context, url string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Accept", "application/vnd.github.v3+json")

	if token := Token(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	return req, nil
}

// Token returns the first non-empty value from GITHUB_TOKEN or GH_TOKEN.
func Token() string {
	for _, key := range []string{"GITHUB_TOKEN", "GH_TOKEN"} {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
	}

	return ""
}
