package server

import "testing"

// score is a test helper mirroring how resolveResources scores a candidate.
func score(query string, fields ...string) float64 {
	return lexicalScore(tokenSet(query), trigramSet(normalizeText(query)), fields...)
}

func TestLexicalScore(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		query  string
		fields []string
		want   func(float64) bool
	}{
		{
			name:   "exact token match on slugged URI ranks high",
			query:  "getting started",
			fields: []string{"panda://getting-started", "Getting Started"},
			want:   func(s float64) bool { return s >= 0.6 },
		},
		{
			name:   "typo still matches via trigrams above threshold",
			query:  "finalty",
			fields: []string{"runbooks://finality_delay", "Investigate Finality Delay"},
			want:   func(s float64) bool { return s >= minResolveScore },
		},
		{
			name:   "unrelated query scores below threshold",
			query:  "account abstraction",
			fields: []string{"networks://active", "Active networks"},
			want:   func(s float64) bool { return s < minResolveScore },
		},
		{
			name:   "empty query scores zero",
			query:  "",
			fields: []string{"panda://getting-started"},
			want:   func(s float64) bool { return s == 0 },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := score(tt.query, tt.fields...)
			if !tt.want(got) {
				t.Fatalf("score(%q, %v) = %v, failed expectation", tt.query, tt.fields, got)
			}
		})
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

func TestLexicalScoreRanksBetterMatchHigher(t *testing.T) {
	t.Parallel()

	// A direct name match must outrank an unrelated resource for the same query.
	relevant := score("finality", "runbooks://finality_delay", "Investigate Finality Delay")
	unrelated := score("finality", "python://ethpandaops", "Python API")

	if relevant <= unrelated {
		t.Fatalf("expected finality runbook (%v) to outrank python docs (%v)", relevant, unrelated)
	}
}
