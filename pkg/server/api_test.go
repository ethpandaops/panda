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
