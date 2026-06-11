package attribution

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestContextRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "value carries", value: "discord:sam", want: "discord:sam"},
		{name: "whitespace trimmed", value: "  discord:sam ", want: "discord:sam"},
		{name: "empty leaves context unchanged", value: "", want: ""},
		{name: "whitespace-only leaves context unchanged", value: "   ", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := WithValue(context.Background(), tt.value)
			assert.Equal(t, tt.want, FromContext(ctx))
		})
	}
}

func TestFromContextDefaultsEmpty(t *testing.T) {
	assert.Empty(t, FromContext(context.Background()))
}
