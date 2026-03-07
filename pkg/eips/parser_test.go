package eips

import (
	"testing"
)

func TestParseEIP(t *testing.T) {
	input := []byte(`---
eip: 8037
title: State Creation Gas Cost Increase
description: Harmonization, increase and separate metering of state creation gas costs
author: Maria Silva (@misilva73), Carlos Perez (@CPerezz)
discussions-to: https://ethereum-magicians.org/t/eip-8037
status: Draft
type: Standards Track
category: Core
created: 2025-10-01
requires: 2780, 7702, 7825, 8011
---

## Abstract

This EIP proposes to increase state creation gas costs.
`)

	eip, err := ParseEIP(input)
	if err != nil {
		t.Fatalf("ParseEIP() error: %v", err)
	}

	if eip.Number != 8037 {
		t.Errorf("Number = %d, want 8037", eip.Number)
	}

	if eip.Title != "State Creation Gas Cost Increase" {
		t.Errorf("Title = %q, want %q", eip.Title, "State Creation Gas Cost Increase")
	}

	if eip.Description != "Harmonization, increase and separate metering of state creation gas costs" {
		t.Errorf("Description = %q", eip.Description)
	}

	if eip.Status != "Draft" {
		t.Errorf("Status = %q, want Draft", eip.Status)
	}

	if eip.Type != "Standards Track" {
		t.Errorf("Type = %q, want Standards Track", eip.Type)
	}

	if eip.Category != "Core" {
		t.Errorf("Category = %q, want Core", eip.Category)
	}

	if eip.URL != "https://eips.ethereum.org/EIPS/eip-8037" {
		t.Errorf("URL = %q", eip.URL)
	}

	if eip.Requires != "2780, 7702, 7825, 8011" {
		t.Errorf("Requires = %q", eip.Requires)
	}
}

func TestParseEIP_MinimalFrontmatter(t *testing.T) {
	input := []byte(`---
eip: 1
title: EIP Purpose and Guidelines
description: A guide for EIP authors
author: Martin Becze
status: Living
type: Meta
---

Content here.
`)

	eip, err := ParseEIP(input)
	if err != nil {
		t.Fatalf("ParseEIP() error: %v", err)
	}

	if eip.Number != 1 {
		t.Errorf("Number = %d, want 1", eip.Number)
	}

	if eip.Category != "" {
		t.Errorf("Category = %q, want empty", eip.Category)
	}
}

func TestParseEIP_MissingNumber(t *testing.T) {
	input := []byte(`---
title: No Number
description: Missing eip field
---

Content.
`)

	_, err := ParseEIP(input)
	if err == nil {
		t.Fatal("expected error for missing eip number")
	}
}

func TestParseEIP_MissingTitle(t *testing.T) {
	input := []byte(`---
eip: 42
description: Has number but no title
---

Content.
`)

	eip, err := ParseEIP(input)
	if err != nil {
		t.Fatalf("ParseEIP() error: %v", err)
	}

	if eip.Title != "EIP-42" {
		t.Errorf("Title = %q, want %q", eip.Title, "EIP-42")
	}
}

func TestParseEIP_NoFrontmatter(t *testing.T) {
	input := []byte(`# Just markdown, no frontmatter`)

	_, err := ParseEIP(input)
	if err == nil {
		t.Fatal("expected error for missing frontmatter")
	}
}
