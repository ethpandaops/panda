package client

import (
	"testing"
	"time"
)

func TestShouldRefresh(t *testing.T) {
	t.Parallel()

	now := time.Now()
	const buffer = 5 * time.Minute

	tests := []struct {
		name      string
		expiresAt time.Time
		expiresIn int
		fraction  float64
		want      bool
	}{
		{
			name:      "fresh token below the fraction is not refreshed",
			expiresAt: now.Add(40 * time.Minute), // 1h token, 33% elapsed
			expiresIn: 3600,
			fraction:  0.5,
			want:      false,
		},
		{
			name:      "token past 50% of its lifetime is refreshed",
			expiresAt: now.Add(20 * time.Minute), // 1h token, 66% elapsed
			expiresIn: 3600,
			fraction:  0.5,
			want:      true,
		},
		{
			name:      "token exactly at the fraction is not yet refreshed",
			expiresAt: now.Add(30 * time.Minute), // 1h token, exactly 50%
			expiresIn: 3600,
			fraction:  0.5,
			want:      false,
		},
		{
			name:      "within the expiry buffer is always refreshed",
			expiresAt: now.Add(2 * time.Minute),
			expiresIn: 3600,
			fraction:  0.5,
			want:      true,
		},
		{
			name:      "a higher fraction refreshes later",
			expiresAt: now.Add(20 * time.Minute), // 66% elapsed
			expiresIn: 3600,
			fraction:  0.75, // refresh only past 75%
			want:      false,
		},
		{
			name:      "unknown lifetime falls back to the buffer only",
			expiresAt: now.Add(20 * time.Minute),
			expiresIn: 0,
			fraction:  0.5,
			want:      false,
		},
		{
			name:      "zero expiry never triggers",
			expiresAt: time.Time{},
			expiresIn: 3600,
			fraction:  0.5,
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := ShouldRefresh(now, tt.expiresAt, tt.expiresIn, buffer, tt.fraction)
			if got != tt.want {
				t.Fatalf("ShouldRefresh = %v, want %v", got, tt.want)
			}
		})
	}
}
