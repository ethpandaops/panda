package cli

import (
	"errors"
	"strings"
	"testing"
)

func TestRedirectLokiError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		// wantRedirect is true when the Loki module is unavailable and the error
		// should be replaced with the ClickHouse redirect synopsis; false when a
		// genuine failure must be returned untouched.
		wantRedirect bool
	}{
		{
			name:         "module not enabled (404) redirects",
			err:          &apiError{Status: 404, Message: `module "loki" is not enabled`},
			wantRedirect: true,
		},
		{
			name:         "not available message redirects",
			err:          &apiError{Status: 503, Message: "loki datasource not available"},
			wantRedirect: true,
		},
		{
			name:         "genuine query failure against live loki is not redirected",
			err:          &apiError{Status: 400, Message: "parse error: unexpected token"},
			wantRedirect: false,
		},
		{
			name:         "non-apiError is returned untouched",
			err:          errors.New("connection refused"),
			wantRedirect: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := redirectLokiError(tt.err)

			if hasSynopsis := strings.Contains(got.Error(), lokiRedirectSynopsis); hasSynopsis != tt.wantRedirect {
				t.Errorf("lokiRedirectSynopsis present = %v, want %v\nerror: %s", hasSynopsis, tt.wantRedirect, got)
			}

			if !tt.wantRedirect && got != tt.err {
				t.Errorf("expected error returned untouched, got rewrapped: %s", got)
			}
		})
	}
}
