package cli

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestComputeExecDeadline(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		timeout string
		want    time.Duration
	}{
		{name: "unset uses the server default budget", timeout: "", want: 90 * time.Second},
		{name: "explicit timeout adds headroom", timeout: "2m", want: 3 * time.Minute},
		{name: "max guest timeout adds headroom", timeout: "5m", want: 6 * time.Minute},
		{name: "unparseable falls back to the default budget", timeout: "bogus", want: 90 * time.Second},
		{name: "non-positive falls back to the default budget", timeout: "-10s", want: 90 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, computeExecDeadline(tt.timeout))
		})
	}
}
