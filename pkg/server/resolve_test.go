package server

import (
	"testing"

	"github.com/ethpandaops/panda/pkg/textmatch"
)

func TestResolveScoreThreshold(t *testing.T) {
	t.Parallel()

	// The shared lexical scorer must keep typo matches above and unrelated
	// matches below minResolveScore, or resolveResources mis-filters.
	typo := textmatch.NewQuery("finalty").Score(
		"runbooks://ethereum_protocol_model", "Model the Active Fork and Judge Network Health", "finality thresholds")
	if typo < minResolveScore {
		t.Fatalf("typo score %v fell below resolve threshold %v", typo, minResolveScore)
	}

	unrelated := textmatch.NewQuery("account abstraction").Score("networks://active", "Active networks")
	if unrelated >= minResolveScore {
		t.Fatalf("unrelated score %v reached resolve threshold %v", unrelated, minResolveScore)
	}
}

func TestRefContent(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"runbooks://finalty":          "finalty",
		"consensus-specs://deneb/p2p": "deneb/p2p",
		"finality":                    "finality",
		"network not finalizing":      "network not finalizing",
		"runbooks://":                 "",
	}

	for in, want := range cases {
		if got := refContent(in); got != want {
			t.Errorf("refContent(%q) = %q, want %q", in, got, want)
		}
	}
}
