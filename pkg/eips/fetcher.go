package eips

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ethpandaops/panda/pkg/types"
)

const (
	repoOwner     = "ethereum"
	repoName      = "EIPs"
	repoBranch    = "master"
	githubAPIBase = "https://api.github.com"
	fetchTimeout  = 5 * time.Minute
)

var (
	eipFilePattern = regexp.MustCompile(`^eip-(\d+)\.md$`)
	getenv         = os.Getenv
)

type fetcher struct {
	client *http.Client
}

func newFetcher() *fetcher {
	return &fetcher{
		client: &http.Client{Timeout: fetchTimeout},
	}
}

func (f *fetcher) latestCommitSHA(ctx context.Context) (string, error) {
	url := fmt.Sprintf(
		"%s/repos/%s/%s/git/ref/heads/%s",
		githubAPIBase, repoOwner, repoName, repoBranch,
	)

	req, err := newRequest(ctx, url)
	if err != nil {
		return "", err
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching commit SHA: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf(
			"unexpected status %d fetching commit SHA", resp.StatusCode,
		)
	}

	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&ref); err != nil {
		return "", fmt.Errorf("decoding commit SHA response: %w", err)
	}

	return ref.Object.SHA, nil
}

func (f *fetcher) fetchAll(ctx context.Context) ([]types.EIP, error) {
	url := fmt.Sprintf(
		"%s/repos/%s/%s/tarball/%s",
		githubAPIBase, repoOwner, repoName, repoBranch,
	)

	req, err := newRequest(ctx, url)
	if err != nil {
		return nil, err
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching tarball: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf(
			"unexpected status %d fetching tarball", resp.StatusCode,
		)
	}

	return extractEIPs(resp.Body)
}

func extractEIPs(r io.Reader) ([]types.EIP, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("creating gzip reader: %w", err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)

	var eips []types.EIP

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}

		if err != nil {
			return nil, fmt.Errorf("reading tar entry: %w", err)
		}

		if header.Typeflag != tar.TypeReg {
			continue
		}

		dir := filepath.Base(filepath.Dir(header.Name))
		filename := filepath.Base(header.Name)

		if dir != "EIPS" || !eipFilePattern.MatchString(filename) {
			continue
		}

		data, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", header.Name, err)
		}

		eip, err := ParseEIP(data)
		if err != nil {
			continue
		}

		eips = append(eips, eip)
	}

	return eips, nil
}

func newRequest(ctx context.Context, url string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Accept", "application/vnd.github.v3+json")

	if token := githubToken(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	return req, nil
}

func githubToken() string {
	for _, key := range []string{"GITHUB_TOKEN", "GH_TOKEN"} {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
	}

	return ""
}
