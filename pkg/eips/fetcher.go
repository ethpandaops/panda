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
	"strings"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/mcp/pkg/types"
)

const (
	repoOwner     = "ethereum"
	repoName      = "EIPs"
	repoBranch    = "master"
	githubAPIBase = "https://api.github.com"
	fetchTimeout  = 5 * time.Minute
)

type fetcher struct {
	client *http.Client
	log    logrus.FieldLogger
}

func newFetcher(log logrus.FieldLogger) *fetcher {
	return &fetcher{
		client: &http.Client{Timeout: fetchTimeout},
		log:    log.WithField("component", "eip_fetcher"),
	}
}

// latestCommitSHA returns the latest commit SHA for the repo's default branch.
func (f *fetcher) latestCommitSHA(ctx context.Context) (string, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/git/ref/heads/%s", githubAPIBase, repoOwner, repoName, repoBranch)

	req, err := f.newRequest(ctx, url)
	if err != nil {
		return "", err
	}

	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := f.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching commit SHA: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status %d from GitHub API", resp.StatusCode)
	}

	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&ref); err != nil {
		return "", fmt.Errorf("decoding ref response: %w", err)
	}

	return ref.Object.SHA, nil
}

// fetchAll downloads the repo tarball and extracts all EIP frontmatter.
func (f *fetcher) fetchAll(ctx context.Context) ([]types.EIP, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/tarball/%s", githubAPIBase, repoOwner, repoName, repoBranch)

	req, err := f.newRequest(ctx, url)
	if err != nil {
		return nil, err
	}

	f.log.Info("Downloading EIPs tarball from GitHub...")

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloading tarball: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d downloading tarball", resp.StatusCode)
	}

	return f.extractEIPs(resp.Body)
}

// extractEIPs reads a gzipped tarball and parses all EIP markdown files.
func (f *fetcher) extractEIPs(r io.Reader) ([]types.EIP, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("creating gzip reader: %w", err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)

	var eips []types.EIP

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}

		if err != nil {
			return nil, fmt.Errorf("reading tar entry: %w", err)
		}

		if hdr.Typeflag != tar.TypeReg {
			continue
		}

		// Match paths like: ethereum-EIPs-abc1234/EIPS/eip-1.md
		dir := filepath.Dir(hdr.Name)
		base := filepath.Base(hdr.Name)

		if filepath.Base(dir) != "EIPS" {
			continue
		}

		if !strings.HasPrefix(base, "eip-") || !strings.HasSuffix(base, ".md") {
			continue
		}

		data, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", hdr.Name, err)
		}

		eip, err := ParseEIP(data)
		if err != nil {
			f.log.WithError(err).WithField("file", base).Debug("Skipping unparseable EIP")

			continue
		}

		eips = append(eips, eip)
	}

	return eips, nil
}

func (f *fetcher) newRequest(ctx context.Context, url string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		req.Header.Set("Authorization", "token "+token)
	}

	return req, nil
}
