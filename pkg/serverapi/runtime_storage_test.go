package serverapi

import "testing"

func TestRuntimeStorageScopedPrefix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		executionID string
		prefix      string
		want        string
	}{
		{
			name:        "empty prefix",
			executionID: "exec-123",
			prefix:      "",
			want:        "exec-123/",
		},
		{
			name:        "relative prefix",
			executionID: "exec-123",
			prefix:      "charts/",
			want:        "exec-123/charts/",
		},
		{
			name:        "current execution prefix",
			executionID: "exec-123",
			prefix:      "exec-123/charts/",
			want:        "exec-123/charts/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := RuntimeStorageScopedPrefix(tt.executionID, tt.prefix); got != tt.want {
				t.Fatalf("RuntimeStorageScopedPrefix() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRuntimeStorageScopedKey(t *testing.T) {
	t.Parallel()

	scopedKey, relativeKey, err := RuntimeStorageScopedKey("exec-123", "exec-123/reports/chart.png")
	if err != nil {
		t.Fatalf("RuntimeStorageScopedKey returned error: %v", err)
	}

	if scopedKey != "exec-123/reports/chart.png" {
		t.Fatalf("unexpected scoped key: %q", scopedKey)
	}

	if relativeKey != "reports/chart.png" {
		t.Fatalf("unexpected relative key: %q", relativeKey)
	}
}
