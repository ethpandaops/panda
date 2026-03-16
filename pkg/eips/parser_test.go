package eips

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseEIP(t *testing.T) {
	data := []byte(`---
eip: 1559
title: Fee market change for ETH 1.0 chain
description: A transaction pricing mechanism with a base fee
author: Vitalik Buterin (@vbuterin), Eric Conner (@econoar)
status: Final
type: Standards Track
category: Core
created: 2019-04-13
requires: 2930
---
This EIP introduces a base fee per gas in blocks.
`)

	eip, err := ParseEIP(data)
	require.NoError(t, err)
	assert.Equal(t, 1559, eip.Number)
	assert.Equal(t, "Fee market change for ETH 1.0 chain", eip.Title)
	assert.Equal(t, "A transaction pricing mechanism with a base fee", eip.Description)
	assert.Equal(t, "Final", eip.Status)
	assert.Equal(t, "Standards Track", eip.Type)
	assert.Equal(t, "Core", eip.Category)
	assert.Equal(t, "2019-04-13", eip.Created)
	assert.Equal(t, "2930", eip.Requires)
	assert.Contains(t, eip.Content, "base fee per gas")
	assert.Equal(t, "https://eips.ethereum.org/EIPS/eip-1559", eip.URL)
}

func TestParseEIP_MinimalFrontmatter(t *testing.T) {
	data := []byte(`---
eip: 100
title: Some EIP
status: Draft
type: Informational
---
Body text.
`)

	eip, err := ParseEIP(data)
	require.NoError(t, err)
	assert.Equal(t, 100, eip.Number)
	assert.Equal(t, "Some EIP", eip.Title)
	assert.Equal(t, "", eip.Category)
}

func TestParseEIP_MissingNumber(t *testing.T) {
	data := []byte(`---
title: No number
status: Draft
type: Informational
---
Body.
`)

	_, err := ParseEIP(data)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing or zero eip number")
}

func TestParseEIP_MissingTitle(t *testing.T) {
	data := []byte(`---
eip: 42
status: Draft
type: Informational
---
Body.
`)

	eip, err := ParseEIP(data)
	require.NoError(t, err)
	assert.Equal(t, "EIP-42", eip.Title)
}

func TestParseEIP_NoFrontmatter(t *testing.T) {
	data := []byte(`Just some text without frontmatter.`)

	_, err := ParseEIP(data)
	require.Error(t, err)
}
