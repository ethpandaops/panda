package github

import (
	"context"
	"io"
	"net/http"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLatestReleaseSuccessAndFailures(t *testing.T) {
	checker := NewReleaseChecker("ethpandaops", "panda")

	checker.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.Path {
			case "/repos/ethpandaops/panda/releases/latest":
				return &http.Response{
					StatusCode: http.StatusOK,
					Body: io.NopCloser(strings.NewReader(`{
						"tag_name":"v1.2.3",
						"html_url":"https://example.com/release",
						"assets":[{"name":"panda_1.2.3_` + runtime.GOOS + `_` + runtime.GOARCH + `.tar.gz","browser_download_url":"https://example.com/panda.tar.gz"},{"name":"checksums.txt","browser_download_url":"https://example.com/checksums.txt"}]
					}`)),
					Header: make(http.Header),
				}, nil
			default:
				return &http.Response{
					StatusCode: http.StatusNotFound,
					Body:       io.NopCloser(strings.NewReader("missing")),
					Header:     make(http.Header),
				}, nil
			}
		}),
	}

	release, err := checker.LatestRelease(context.Background())
	require.NoError(t, err)
	require.NotNil(t, release)
	assert.Equal(t, "v1.2.3", release.TagName)

	asset, err := release.FindCurrentPlatformAsset()
	require.NoError(t, err)
	assert.Contains(t, asset.Name, runtime.GOOS)

	checksums, err := release.ChecksumsAsset()
	require.NoError(t, err)
	assert.Equal(t, "checksums.txt", checksums.Name)

	_, err = release.FindAsset("plan9", "arm", RepoName)
	require.Error(t, err)

	checker.httpClient = &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusBadGateway,
				Body:       io.NopCloser(strings.NewReader("bad gateway")),
				Header:     make(http.Header),
			}, nil
		}),
	}

	_, err = checker.LatestRelease(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "github API returned 502")
}

type roundTripFunc func(req *http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}
