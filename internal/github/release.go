// Package github provides a client for checking GitHub releases and
// a file-based cache for update notifications.
package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"time"
)

const (
	// RepoOwner is the GitHub organization that owns the panda repository.
	RepoOwner = "ethpandaops"

	// RepoName is the GitHub repository name.
	RepoName = "panda"

	apiTimeout = 10 * time.Second
)

// Release represents a GitHub release.
type Release struct {
	TagName string  `json:"tag_name"`
	HTMLURL string  `json:"html_url"`
	Assets  []Asset `json:"assets"`
}

// Asset represents a downloadable file attached to a release.
type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// ReleaseChecker fetches release information from the GitHub API.
type ReleaseChecker struct {
	owner      string
	repo       string
	httpClient *http.Client
}

// NewReleaseChecker creates a checker for the given GitHub owner/repo.
func NewReleaseChecker(owner, repo string) *ReleaseChecker {
	return &ReleaseChecker{
		owner: owner,
		repo:  repo,
		httpClient: &http.Client{
			Timeout: apiTimeout,
		},
	}
}

// LatestRelease fetches the most recent non-prerelease release.
func (r *ReleaseChecker) LatestRelease(ctx context.Context) (*Release, error) {
	url := fmt.Sprintf(
		"https://api.github.com/repos/%s/%s/releases/latest",
		r.owner, r.repo,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching latest release: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github API returned %d", resp.StatusCode)
	}

	var release Release
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, fmt.Errorf("decoding release: %w", err)
	}

	return &release, nil
}

// FindAsset returns the binary asset for the given OS, architecture, and
// binary name. Asset names follow the goreleaser pattern:
// {binaryName}_{version}_{os}_{arch}.tar.gz
func (r *Release) FindAsset(goos, goarch, binaryName string) (*Asset, error) {
	version := strings.TrimPrefix(r.TagName, "v")
	name := fmt.Sprintf("%s_%s_%s_%s.tar.gz", binaryName, version, goos, goarch)

	for i := range r.Assets {
		if r.Assets[i].Name == name {
			return &r.Assets[i], nil
		}
	}

	return nil, fmt.Errorf(
		"no asset found for %s/%s (expected %s)",
		goos, goarch, name,
	)
}

// FindCurrentPlatformAsset returns the asset for the CLI binary
// matching the current runtime.
func (r *Release) FindCurrentPlatformAsset() (*Asset, error) {
	return r.FindAsset(runtime.GOOS, runtime.GOARCH, RepoName)
}

// ChecksumsAsset returns the checksums.txt asset from the release.
func (r *Release) ChecksumsAsset() (*Asset, error) {
	for i := range r.Assets {
		if r.Assets[i].Name == "checksums.txt" {
			return &r.Assets[i], nil
		}
	}

	return nil, fmt.Errorf("no checksums.txt asset found in release %s", r.TagName)
}
