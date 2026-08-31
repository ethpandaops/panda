package server

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSearchErrorStatusClassifiesServiceConditions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want int
	}{
		{
			name: "embed upstream unreachable is a service condition",
			err:  errors.New(`search failed: embedding query: calling proxy embed: Post "https://proxy/embed": context deadline exceeded`),
			want: http.StatusServiceUnavailable,
		},
		{
			name: "warming index is a service condition",
			err:  errors.New("search failed: example index not ready"),
			want: http.StatusServiceUnavailable,
		},
		{
			name: "other search failures stay caller errors",
			err:  errors.New("search failed: unknown category"),
			want: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, searchErrorStatus(tt.err))
		})
	}
}

func TestSearchErrorStatusCoversInactiveIndexes(t *testing.T) {
	t.Parallel()

	// searchsvc reports a never-activated index differently from a warming
	// one; both are service conditions the caller cannot fix by rewording.
	for _, message := range []string{
		"example search index not available",
		"runbook search index not available",
		"EIP search index not available",
		"consensus specs search index not available",
	} {
		assert.Equal(t, http.StatusServiceUnavailable, searchErrorStatus(errors.New(message)), message)
	}

	// Filter-value rejections remain the caller's to fix.
	for _, message := range []string{
		`unknown category: "bogus". Available categories: a, b`,
		`unknown tag: "bogus". Available tags: a, b`,
	} {
		assert.Equal(t, http.StatusBadRequest, searchErrorStatus(errors.New(message)), message)
	}
}
