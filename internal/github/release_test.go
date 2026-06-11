package github

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReleaseFindAsset(t *testing.T) {
	release := &Release{
		TagName: "v1.2.3",
		Assets: []Asset{
			{Name: "panda_1.2.3_linux_amd64.tar.gz", BrowserDownloadURL: "https://example.com/linux-amd64"},
			{Name: "panda_1.2.3_darwin_arm64.tar.gz", BrowserDownloadURL: "https://example.com/darwin-arm64"},
			{Name: "checksums.txt", BrowserDownloadURL: "https://example.com/checksums"},
		},
	}

	tests := []struct {
		name       string
		goos       string
		goarch     string
		binaryName string
		wantName   string
		wantErr    bool
	}{
		{
			name:       "matches linux amd64",
			goos:       "linux",
			goarch:     "amd64",
			binaryName: "panda",
			wantName:   "panda_1.2.3_linux_amd64.tar.gz",
		},
		{
			name:       "matches darwin arm64",
			goos:       "darwin",
			goarch:     "arm64",
			binaryName: "panda",
			wantName:   "panda_1.2.3_darwin_arm64.tar.gz",
		},
		{
			name:       "no asset for unknown platform",
			goos:       "windows",
			goarch:     "amd64",
			binaryName: "panda",
			wantErr:    true,
		},
		{
			name:       "no asset for unknown binary name",
			goos:       "linux",
			goarch:     "amd64",
			binaryName: "other",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			asset, err := release.FindAsset(tt.goos, tt.goarch, tt.binaryName)
			if tt.wantErr {
				require.Error(t, err)
				assert.Nil(t, asset)

				return
			}

			require.NoError(t, err)
			require.NotNil(t, asset)
			assert.Equal(t, tt.wantName, asset.Name)
		})
	}
}

func TestReleaseFindAssetStripsVersionPrefix(t *testing.T) {
	release := &Release{
		TagName: "v2.0.0",
		Assets: []Asset{
			{Name: "panda_2.0.0_linux_amd64.tar.gz"},
		},
	}

	asset, err := release.FindAsset("linux", "amd64", "panda")
	require.NoError(t, err)
	assert.Equal(t, "panda_2.0.0_linux_amd64.tar.gz", asset.Name)
}

func TestReleaseChecksumsAsset(t *testing.T) {
	t.Run("present", func(t *testing.T) {
		release := &Release{
			TagName: "v1.0.0",
			Assets: []Asset{
				{Name: "panda_1.0.0_linux_amd64.tar.gz"},
				{Name: "checksums.txt", BrowserDownloadURL: "https://example.com/checksums"},
			},
		}

		asset, err := release.ChecksumsAsset()
		require.NoError(t, err)
		require.NotNil(t, asset)
		assert.Equal(t, "checksums.txt", asset.Name)
	})

	t.Run("missing", func(t *testing.T) {
		release := &Release{
			TagName: "v1.0.0",
			Assets: []Asset{
				{Name: "panda_1.0.0_linux_amd64.tar.gz"},
			},
		}

		asset, err := release.ChecksumsAsset()
		require.Error(t, err)
		assert.Nil(t, asset)
	})
}

func TestFirstPublished(t *testing.T) {
	t.Run("skips drafts, keeps pre-releases", func(t *testing.T) {
		releases := []Release{
			{TagName: "v1.3.0", Draft: true},
			{TagName: "v1.3.0-rc.1", Prerelease: true},
			{TagName: "v1.2.0"},
		}

		release := firstPublished(releases)
		require.NotNil(t, release)
		assert.Equal(t, "v1.3.0-rc.1", release.TagName)
	})

	t.Run("stable first when newest", func(t *testing.T) {
		releases := []Release{
			{TagName: "v1.3.0"},
			{TagName: "v1.3.0-rc.1", Prerelease: true},
		}

		release := firstPublished(releases)
		require.NotNil(t, release)
		assert.Equal(t, "v1.3.0", release.TagName)
	})

	t.Run("no published releases", func(t *testing.T) {
		assert.Nil(t, firstPublished([]Release{{TagName: "v1.0.0", Draft: true}}))
		assert.Nil(t, firstPublished(nil))
	})
}
