package cli

import (
	"errors"
	"strings"
	"testing"
)

func TestWrapClickHouseError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		// wantReplace is true when the generic hint should be swapped for the
		// table-specific clickHouseUnknownTableHint; false when the error must be
		// returned untouched so the original (richer) hint flows through.
		wantReplace bool
	}{
		{
			name:        "unknown table replaces generic hint",
			err:         &apiError{Status: 404, Message: "Code: 60. DB::Exception: Unknown table expression identifier 'otel_logs'. (UNKNOWN_TABLE)"},
			wantReplace: true,
		},
		{
			name:        "unknown database replaces generic hint",
			err:         &apiError{Status: 404, Message: "Code: 81. DB::Exception: Database bogusdb does not exist. (UNKNOWN_DATABASE)"},
			wantReplace: true,
		},
		{
			name:        "unknown datasource is returned untouched",
			err:         &apiError{Status: 404, Message: `clickhouse datasource "nonexistent" not found`},
			wantReplace: false,
		},
		{
			name:        "non-apiError is returned untouched",
			err:         errors.New("connection refused"),
			wantReplace: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := wrapClickHouseError(tt.err)

			if hasHint := strings.Contains(got.Error(), clickHouseUnknownTableHint); hasHint != tt.wantReplace {
				t.Errorf("clickHouseUnknownTableHint present = %v, want %v\nerror: %s", hasHint, tt.wantReplace, got)
			}

			if !tt.wantReplace && got != tt.err {
				t.Errorf("expected error returned untouched, got rewrapped: %s", got)
			}
		})
	}
}
