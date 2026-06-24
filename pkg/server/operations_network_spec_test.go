package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sampleSpecMarkdown = `# glamsterdam-devnet-5 Spec Summary

## Overview

First glamsterdam-prefixed CL+EL devnet.

## EIP List

| EIP | Title | Status |
|-----|-------|--------|
| EIP-7708 | ETH transfers emit a log | |
| EIP-7732 | Enshrined Proposer-Builder Separation | Updated |

## Local testing

Kurtosis example:
` + "```" + `yaml title="kurtosis.yaml"
network_params:
  gloas_fork_epoch: 1
  genesis_gaslimit: 150000000
` + "```" + `
`

func TestParseNetworkSpec(t *testing.T) {
	t.Parallel()

	spec := parseNetworkSpec("glamsterdam-devnet-5", "https://notes.ethereum.org/@ethpandaops/glamsterdam-devnet-5", sampleSpecMarkdown)

	assert.Equal(t, "glamsterdam-devnet-5", spec.Network)
	assert.Equal(t, "glamsterdam-devnet-5 Spec Summary", spec.Title)
	assert.Equal(t, sampleSpecMarkdown, spec.Markdown)

	headings := make([]string, 0, len(spec.Sections))
	for _, section := range spec.Sections {
		headings = append(headings, section.Heading)
	}
	assert.Equal(t, []string{"Overview", "EIP List", "Local testing"}, headings)

	require.Len(t, spec.Sections, 3)
	assert.Contains(t, spec.Sections[0].Content, "First glamsterdam-prefixed")

	// Section content is kept verbatim — table and code fences included.
	assert.Contains(t, spec.Sections[1].Content, "| EIP | Title | Status |")
	local := spec.Sections[2]
	assert.Contains(t, local.Content, "gloas_fork_epoch: 1")
	assert.Contains(t, local.Content, "```yaml")
}

func TestParseNetworkSpecNoSections(t *testing.T) {
	t.Parallel()

	spec := parseNetworkSpec("x", "https://notes.ethereum.org/@ethpandaops/x", "# Title\n\nJust prose, no sections.\n")

	assert.Equal(t, "Title", spec.Title)
	assert.Empty(t, spec.Sections)
	assert.Contains(t, spec.Markdown, "Just prose")
}

func TestResolveNetworkSpecURL(t *testing.T) {
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

			got, err := resolveNetworkSpecURL(tt.id, tt.override)
			if tt.wantErr {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
