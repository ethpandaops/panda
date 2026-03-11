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

func TestParseRuntimeStorageListScopesToExecution(t *testing.T) {
	t.Parallel()

	data := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<ListBucketResult>
  <Contents>
    <Key>exec-123/reports/chart.png</Key>
    <Size>12</Size>
    <LastModified>2026-03-11T12:00:00Z</LastModified>
  </Contents>
  <Contents>
    <Key>other-exec/secret.csv</Key>
    <Size>7</Size>
    <LastModified>2026-03-11T12:01:00Z</LastModified>
  </Contents>
</ListBucketResult>`)

	files, nextToken, err := ParseRuntimeStorageList(data, "exec-123", func(key string) string {
		return "https://files.example/" + key
	})
	if err != nil {
		t.Fatalf("ParseRuntimeStorageList returned error: %v", err)
	}

	if nextToken != "" {
		t.Fatalf("unexpected continuation token: %q", nextToken)
	}

	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}

	if files[0].Key != "reports/chart.png" {
		t.Fatalf("unexpected file key: %q", files[0].Key)
	}

	if files[0].URL != "https://files.example/exec-123/reports/chart.png" {
		t.Fatalf("unexpected file URL: %q", files[0].URL)
	}
}
