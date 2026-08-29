package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAPIErrorMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "plain error field",
			body: `{"error":"sandbox not found"}`,
			want: "sandbox not found",
		},
		{
			name: "error with code and request id",
			body: `{"error":"guest unreachable","code":"guest_unreachable","request_id":"req-123"}`,
			want: "guest unreachable (code=guest_unreachable, request_id=req-123)",
		},
		{
			name: "error with code only",
			body: `{"error":"guest unreachable","code":"guest_unreachable"}`,
			want: "guest unreachable (code=guest_unreachable)",
		},
		{
			name: "error with request id only",
			body: `{"error":"internal error","request_id":"req-456"}`,
			want: "internal error (request_id=req-456)",
		},
		{
			name: "problem json detail",
			body: `{"detail":"upstream failed","title":"Bad Gateway"}`,
			want: "upstream failed",
		},
		{
			name: "non-string structured fields are ignored",
			body: `{"error":"boom","code":42}`,
			want: "boom",
		},
		{
			name: "non-json falls back to raw body",
			body: "404 page not found\n",
			want: "404 page not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, apiErrorMessage([]byte(tt.body)))
		})
	}
}
