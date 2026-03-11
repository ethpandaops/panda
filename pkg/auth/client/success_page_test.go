package client

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecodePandaASCII(t *testing.T) {
	t.Parallel()

	art := decodePandaASCII()

	assert.NotEmpty(t, art)
}

func TestBuildSuccessPage_Default(t *testing.T) {
	t.Parallel()

	page := buildSuccessPage(callbackUser{
		Login:     "testuser",
		AvatarURL: "https://example.com/avatar.png",
	})

	assert.Contains(t, page, "testuser")
	assert.Contains(t, page, "Authenticated")
	assert.Contains(t, page, "logged in to panda")
	assert.Contains(t, page, "avatar.png")
	assert.Contains(t, page, "panda datasources")
	assert.NotContains(t, page, "ethpandaops")
	assert.NotContains(t, page, "casino-royale")
	assert.NotContains(t, page, "<pre")
}

func TestBuildSuccessPage_EthPandaOps(t *testing.T) {
	t.Parallel()

	page := buildSuccessPage(callbackUser{
		Login:     "pandafan",
		AvatarURL: "https://example.com/avatar.png",
		Orgs:      []string{"ethpandaops"},
	})

	assert.Contains(t, page, "pandafan")
	assert.Contains(t, page, "Enjoy debugging your devnet champ")
	assert.Contains(t, page, "ethpandaops")
	assert.Contains(t, page, "casino-royale-bond.gif")
	assert.NotContains(t, page, "<pre")
}

func TestBuildSuccessPage_SpecialUser(t *testing.T) {
	t.Parallel()

	for _, login := range []string{"samcm", "mattevans", "savid"} {
		t.Run(login, func(t *testing.T) {
			t.Parallel()

			page := buildSuccessPage(callbackUser{
				Login:     login,
				AvatarURL: "https://example.com/avatar.png",
				Orgs:      []string{"ethpandaops"},
			})

			assert.Contains(t, page, login)
			assert.Contains(t, page, "Enjoy debugging your devnet champ")
			assert.Contains(t, page, "<pre")
			assert.NotContains(t, page, "casino-royale-bond.gif")
		})
	}
}

func TestBuildSuccessPage_SpecialUserWithoutOrg(t *testing.T) {
	t.Parallel()

	page := buildSuccessPage(callbackUser{
		Login: "samcm",
		Orgs:  []string{"some-other-org"},
	})

	assert.Contains(t, page, "logged in to panda")
	assert.NotContains(t, page, "<pre")
	assert.NotContains(t, page, "casino-royale")
}

func TestBuildSuccessPage_CaseInsensitiveOrg(t *testing.T) {
	t.Parallel()

	page := buildSuccessPage(callbackUser{
		Login: "someone",
		Orgs:  []string{"EthPandaOps"},
	})

	assert.Contains(t, page, "Enjoy debugging your devnet champ")
}

func TestBuildSuccessPage_NoAvatar(t *testing.T) {
	t.Parallel()

	page := buildSuccessPage(callbackUser{
		Login: "noavatar",
	})

	assert.Contains(t, page, "avatar-fallback")
	assert.Contains(t, page, ">N<")
}

func TestBuildSuccessPage_EmptyLogin(t *testing.T) {
	t.Parallel()

	page := buildSuccessPage(callbackUser{})

	assert.Contains(t, page, "user")
	assert.Contains(t, page, "Authenticated")
}

func TestHasOrg(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		orgs   []string
		target string
		want   bool
	}{
		{"exact match", []string{"ethpandaops"}, "ethpandaops", true},
		{"case insensitive", []string{"EthPandaOps"}, "ethpandaops", true},
		{"no match", []string{"other-org"}, "ethpandaops", false},
		{"empty orgs", []string{}, "ethpandaops", false},
		{"multiple orgs", []string{"foo", "ethpandaops", "bar"}, "ethpandaops", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, hasOrg(tt.orgs, tt.target))
		})
	}
}

// TestBuildSuccessPage_WritePreview writes HTML files to /tmp/panda-auth-preview/
// for visual inspection. Run with:
//
//	go test -run TestBuildSuccessPage_WritePreview -v ./pkg/auth/client/
//
// Then open the printed file paths in your browser.
func TestBuildSuccessPage_WritePreview(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(os.TempDir(), "panda-auth-preview")
	require.NoError(t, os.MkdirAll(dir, 0o755))

	cases := map[string]callbackUser{
		"default.html": {
			Login:     "randomdev",
			AvatarURL: "https://avatars.githubusercontent.com/u/1?v=4",
		},
		"ethpandaops.html": {
			Login:     "pandafan",
			AvatarURL: "https://avatars.githubusercontent.com/u/1?v=4",
			Orgs:      []string{"ethpandaops"},
		},
		"special_user.html": {
			Login:     "samcm",
			AvatarURL: "https://avatars.githubusercontent.com/u/1?v=4",
			Orgs:      []string{"ethpandaops"},
		},
	}

	var paths []string

	for name, user := range cases {
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, []byte(buildSuccessPage(user)), 0o644))

		paths = append(paths, p)
	}

	t.Logf("\n\nPreview files written. Open in browser:\n\n  %s\n", strings.Join(paths, "\n  "))
}
