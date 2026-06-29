package networkspec

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sampleSpecMarkdown = `# glamsterdam-devnet-5 Spec Summary

:::info
glamsterdam-devnet-5 targets to launch soon.
:::

:::info
| Contract | Req | type | Address |
|----------|---------|------|--------------------------------------------|
| Builder | deposit | 0x03 | 0x0000884d2AA32eAa155F59A2f24eFa73D9008282 |
:::

## Overview

First glamsterdam-prefixed CL+EL devnet.

## EIP List

| EIP | Title | Status |
|-----|-------|--------|
| [EIP-7708](https://eips.ethereum.org/EIPS/eip-7708) [` + "`" + `spec@172188d7` + "`" + `](https://github.com/ethereum/EIPs/blob/172188d7b090ed1afb876140f45e19ac00cba4bb/EIPS/eip-7708.md) | ETH transfers emit a log | |
| EIP-7732 | Enshrined Proposer-Builder Separation | Updated |

### Test Releases

**Consensus Specs:** [v1.7.0-alpha.11](https://github.com/ethereum/consensus-specs/releases/tag/v1.7.0-alpha.11) :heavy_check_mark:
**Execution Spec Tests:** v4.5.0

### Spec versions required & Open PRs

**Execution Specs**
- [#2990 - feat(spec,tests): Implement EIP-8282](https://github.com/ethereum/execution-specs/pull/2990) Open :exclamation:
- [#3017 - EIP-2780 implement resource-based intrinsic gas](https://github.com/ethereum/execution-specs/pull/3017) Merged :heavy_check_mark:

## Local testing

Kurtosis example:
` + "```" + `yaml title="kurtosis.yaml"
participants_matrix:
  el:
    - el_type: geth
      el_image: ethpandaops/geth:glamsterdam-devnet-5
network_params:
  gloas_fork_epoch: 1
  genesis_gaslimit: 150000000
additional_services:
  - dora
` + "```" + `

## Metrics

https://notes.ethereum.org/@ethpandaops/bal-otel

Previous devnet spec sheets for reference: https://notes.ethereum.org/@ethpandaops/glamsterdam-devnet-4
`

func TestParse(t *testing.T) {
	t.Parallel()

	spec := Parse("glamsterdam-devnet-5", "https://notes.ethereum.org/@ethpandaops/glamsterdam-devnet-5", sampleSpecMarkdown)

	assert.Equal(t, "glamsterdam-devnet-5", spec.Network)
	assert.Equal(t, "glamsterdam-devnet-5 Spec Summary", spec.Title)
	assert.Contains(t, spec.Notices[0], "targets to launch")

	require.Len(t, spec.EIPs, 2)
	assert.Equal(t, "EIP-7708", spec.EIPs[0].ID)
	assert.Equal(t, "ETH transfers emit a log", spec.EIPs[0].Title)
	assert.Equal(t, "EIP-7732", spec.EIPs[1].ID)
	assert.Contains(t, spec.EIPs[1].Flags, "updated")
	assert.Equal(t, "https://eips.ethereum.org/EIPS/eip-7708", spec.EIPs[0].URL)
	assert.Equal(t, "https://github.com/ethereum/EIPs/blob/172188d7b090ed1afb876140f45e19ac00cba4bb/EIPS/eip-7708.md", spec.EIPs[0].SpecURL)
	assert.Equal(t, "https://eips.ethereum.org/EIPS/eip-7732", spec.EIPs[1].URL)

	require.Len(t, spec.SystemContracts, 1)
	assert.Equal(t, "0x0000884d2AA32eAa155F59A2f24eFa73D9008282", spec.SystemContracts[0].Address)

	require.Len(t, spec.Releases, 2)
	assert.Equal(t, "Consensus Specs", spec.Releases[0].Name)
	assert.Equal(t, "v1.7.0-alpha.11", spec.Releases[0].Version)
	assert.Equal(t, "merged", spec.Releases[0].Status)
	assert.Equal(t, "https://github.com/ethereum/consensus-specs/releases/tag/v1.7.0-alpha.11", spec.Releases[0].URL)
	assert.Equal(t, "Execution Spec Tests", spec.Releases[1].Name)
	assert.Equal(t, "https://github.com/ethereum/execution-spec-tests/releases/tag/v4.5.0", spec.Releases[1].URL)

	require.Len(t, spec.ParticipantImages, 1)
	assert.Equal(t, "geth", spec.ParticipantImages[0].Client)
	assert.Equal(t, "https://hub.docker.com/r/ethpandaops/geth/tags?name=glamsterdam-devnet-5", spec.ParticipantImages[0].URL)
	assert.Equal(t, "https://notes.ethereum.org/@ethpandaops/bal-otel", spec.MetricsURL)
	assert.Equal(t, "https://notes.ethereum.org/@ethpandaops/glamsterdam-devnet-4", spec.PreviousSpecURL)
}

func TestParseNoSections(t *testing.T) {
	t.Parallel()

	spec := Parse("x", "https://notes.ethereum.org/@ethpandaops/x", "# Title\n\nJust prose, no sections.\n")

	assert.Equal(t, "Title", spec.Title)
	assert.Empty(t, spec.EIPs)
	assert.Empty(t, spec.Releases)
}

func TestResolveURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		id       string
		override string
		want     string
		wantErr  bool
	}{
		{
			name: "default convention",
			id:   "glamsterdam-devnet-5",
			want: "https://notes.ethereum.org/@ethpandaops/glamsterdam-devnet-5",
		},
		{
			name:     "allowed override",
			id:       "x",
			override: "https://hackmd.io/abc123",
			want:     "https://hackmd.io/abc123",
		},
		{
			name:     "trailing slash trimmed",
			id:       "x",
			override: "https://notes.ethereum.org/@ethpandaops/x/",
			want:     "https://notes.ethereum.org/@ethpandaops/x",
		},
		{
			name:     "disallowed host",
			id:       "x",
			override: "https://evil.example.com/x",
			wantErr:  true,
		},
		{
			name:     "non-https",
			id:       "x",
			override: "http://notes.ethereum.org/x",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		tt := tt

		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ResolveURL(tt.id, tt.override)
			if tt.wantErr {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFetchSuccess(t *testing.T) {
	t.Parallel()

	var gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte("# spec\n\n## Local testing\n"))
	}))
	defer srv.Close()

	md, status, err := Fetch(context.Background(), http.DefaultClient, srv.URL+"/@ethpandaops/x")

	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, status)
	assert.Contains(t, md, "## Local testing")
	assert.Equal(t, "/@ethpandaops/x/download", gotPath, "should request HackMD /download")
}

func TestFetchDoesNotDoubleSuffixDownload(t *testing.T) {
	t.Parallel()

	var gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	_, status, err := Fetch(context.Background(), http.DefaultClient, srv.URL+"/x/download")

	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, "/x/download", gotPath, "must not append a second /download")
}

func TestFetchNotFound(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, status, err := Fetch(context.Background(), http.DefaultClient, srv.URL+"/missing")

	require.Error(t, err)
	assert.Equal(t, http.StatusNotFound, status)
	assert.Contains(t, err.Error(), "no spec page found")
}

func TestFetchServerError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	_, status, err := Fetch(context.Background(), http.DefaultClient, srv.URL+"/x")

	require.Error(t, err)
	assert.Equal(t, http.StatusBadGateway, status)
}

func TestFetchRejectsOversize(t *testing.T) {
	t.Parallel()

	body := strings.Repeat("a", MaxBytes+10)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	_, status, err := Fetch(context.Background(), http.DefaultClient, srv.URL+"/x")

	require.Error(t, err)
	assert.Equal(t, http.StatusBadGateway, status)
	assert.Contains(t, err.Error(), "exceeded")
}
