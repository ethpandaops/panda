package consensusspecs

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/ethpandaops/panda/internal/githubapi"
	"github.com/ethpandaops/panda/pkg/config"
	"github.com/ethpandaops/panda/pkg/types"
)

const fetchTimeout = 5 * time.Minute

var (
	specFilePattern   = regexp.MustCompile(`^specs/([^/]+)/([^/]+)\.md$`)
	presetFilePattern = regexp.MustCompile(`^presets/mainnet/([^/]+)\.yaml$`)
	configFilePattern = regexp.MustCompile(`^configs/mainnet\.yaml$`)
)

// fetchResult holds the parsed output of a tarball extraction.
type fetchResult struct {
	Specs     []types.ConsensusSpec
	Constants []types.SpecConstant
}

type fetcher struct {
	client *http.Client
	cfg    config.ConsensusSpecsConfig
}

func newFetcher(cfg config.ConsensusSpecsConfig) *fetcher {
	return &fetcher{
		client: &http.Client{Timeout: fetchTimeout},
		cfg:    cfg,
	}
}

// resolveRef determines the git ref to use. If cfg.Ref is empty, it looks up
// the latest GitHub release tag. Returns (ref, error).
func (f *fetcher) resolveRef(ctx context.Context) (string, error) {
	if f.cfg.Ref != "" {
		return f.cfg.Ref, nil
	}

	url := fmt.Sprintf(
		"%s/repos/%s/releases/latest",
		githubapi.APIBase, f.cfg.Repository,
	)

	req, err := githubapi.NewRequest(ctx, url)
	if err != nil {
		return "", err
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching latest release: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf(
			"unexpected status %d fetching latest release", resp.StatusCode,
		)
	}

	var release struct {
		TagName string `json:"tag_name"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", fmt.Errorf("decoding latest release: %w", err)
	}

	if release.TagName == "" {
		return "", fmt.Errorf("latest release has no tag_name")
	}

	return release.TagName, nil
}

// latestCommitSHA resolves the current ref to a commit SHA for cache
// invalidation.
func (f *fetcher) latestCommitSHA(ctx context.Context, ref string) (string, error) {
	// Try as a branch first, then as a tag.
	for _, kind := range []string{"heads", "tags"} {
		url := fmt.Sprintf(
			"%s/repos/%s/git/ref/%s/%s",
			githubapi.APIBase, f.cfg.Repository, kind, ref,
		)

		req, err := githubapi.NewRequest(ctx, url)
		if err != nil {
			return "", err
		}

		resp, err := f.client.Do(req)
		if err != nil {
			return "", fmt.Errorf("fetching commit SHA: %w", err)
		}

		body, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		if readErr != nil {
			return "", fmt.Errorf("reading commit SHA response: %w", readErr)
		}

		if resp.StatusCode != http.StatusOK {
			continue
		}

		var gitRef struct {
			Object struct {
				SHA string `json:"sha"`
			} `json:"object"`
		}

		if err := json.Unmarshal(body, &gitRef); err != nil {
			return "", fmt.Errorf("decoding commit SHA response: %w", err)
		}

		return gitRef.Object.SHA, nil
	}

	return "", fmt.Errorf("could not resolve ref %q to a commit SHA", ref)
}

// fetchAll downloads the tarball for the given ref and extracts specs and
// presets.
func (f *fetcher) fetchAll(ctx context.Context, ref string) (*fetchResult, error) {
	url := fmt.Sprintf(
		"%s/repos/%s/tarball/%s",
		githubapi.APIBase, f.cfg.Repository, ref,
	)

	req, err := githubapi.NewRequest(ctx, url)
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

	return extractConsensusSpecs(resp.Body, f.cfg.Repository, ref)
}

func extractConsensusSpecs(
	r io.Reader,
	repository string,
	ref string,
) (*fetchResult, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("creating gzip reader: %w", err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	result := &fetchResult{}

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

		// Strip the top-level directory prefix from the tarball path.
		relPath := stripTarPrefix(header.Name)

		data, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", header.Name, err)
		}

		// Match spec files: specs/{fork}/{topic}.md
		if m := specFilePattern.FindStringSubmatch(relPath); len(m) == 3 {
			fork, topic := m[1], m[2]
			spec := ParseSpec(fork, topic, data, repository, ref)
			result.Specs = append(result.Specs, spec)

			continue
		}

		// Match preset files: presets/mainnet/{fork}.yaml
		if m := presetFilePattern.FindStringSubmatch(relPath); len(m) == 2 {
			fork := m[1]

			constants, parseErr := ParsePreset(fork, data)
			if parseErr == nil {
				result.Constants = append(result.Constants, constants...)
			}

			continue
		}

		// Match mainnet config: configs/mainnet.yaml
		if configFilePattern.MatchString(relPath) {
			constants, parseErr := ParsePreset("_config", data)
			if parseErr == nil {
				result.Constants = append(result.Constants, constants...)
			}
		}
	}

	return result, nil
}

// stripTarPrefix removes the top-level directory that GitHub adds to tarballs
// (e.g., "ethereum-consensus-specs-abc1234/specs/..." -> "specs/...").
func stripTarPrefix(name string) string {
	parts := strings.SplitN(name, "/", 2)
	if len(parts) < 2 {
		return name
	}

	return parts[1]
}

// specGitHubURL returns the GitHub URL for a spec file.
func specGitHubURL(repository, ref, fork, topic string) string {
	owner, repo := splitRepository(repository)

	return fmt.Sprintf(
		"https://github.com/%s/%s/blob/%s/specs/%s/%s.md",
		owner, repo, ref, fork, topic,
	)
}

func splitRepository(repository string) (string, string) {
	owner := path.Dir(repository)
	repo := path.Base(repository)

	return owner, repo
}
