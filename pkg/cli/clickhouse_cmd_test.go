package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAnsiEscapeStrip(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "no escapes",
			input: "plain log line number=25350000",
			want:  "plain log line number=25350000",
		},
		{
			name:  "reth colour codes",
			input: "\x1b[2m2026-06-19T07:02:48Z\x1b[0m \x1b[32m INFO\x1b[0m reth: hello",
			want:  "2026-06-19T07:02:48Z  INFO reth: hello",
		},
		{
			name:  "preserves multibyte content",
			input: "\x1b[3melapsed\x1b[0m\x1b[2m=\x1b[0m181.734µs",
			want:  "elapsed=181.734µs",
		},
		{
			name:  "preserves tabs between columns",
			input: "ts\t\x1b[32mvalue\x1b[0m",
			want:  "ts\tvalue",
		},
		{
			name:  "osc hyperlink sequence",
			input: "\x1b]8;;https://example.com\x07link\x1b]8;;\x07",
			want:  "link",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ansiEscape.ReplaceAllString(tt.input, "")
			assert.Equal(t, tt.want, got)
		})
	}
}
