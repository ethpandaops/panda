package textmatch

import "testing"

func TestScore(t *testing.T) {
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
			name:   "typo still matches via trigrams",
			query:  "finalty",
			fields: []string{"runbooks://ethereum_protocol_model", "Model the Active Fork and Judge Network Health", "finality thresholds"},
			want:   func(s float64) bool { return s >= 0.2 },
		},
		{
			name:   "unrelated query scores low",
			query:  "account abstraction",
			fields: []string{"networks://active", "Active networks"},
			want:   func(s float64) bool { return s < 0.2 },
		},
		{
			name:   "empty query scores zero",
			query:  "",
			fields: []string{"panda://getting-started"},
			want:   func(s float64) bool { return s == 0 },
		},
		{
			name:   "exact identifier in a long field gets the substring boost",
			query:  "fct_block_head",
			fields: []string{"partition rules and the fct_block_head bridge table"},
			want:   func(s float64) bool { return s >= 0.3 },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := NewQuery(tt.query).Score(tt.fields...)
			if !tt.want(got) {
				t.Fatalf("Score(%q, %v) = %v, failed expectation", tt.query, tt.fields, got)
			}
		})
	}
}

func TestScoreRanksBetterMatchHigher(t *testing.T) {
	t.Parallel()

	q := NewQuery("finality")

	relevant := q.Score("runbooks://ethereum_protocol_model", "finality participation thresholds")
	unrelated := q.Score("python://ethpandaops", "Python API")

	if relevant <= unrelated {
		t.Fatalf("expected finality fields (%v) to outrank python docs (%v)", relevant, unrelated)
	}
}
