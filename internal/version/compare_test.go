package version

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsNewer(t *testing.T) {
	tests := []struct {
		name   string
		local  string
		remote string
		want   bool
	}{
		{"same version", "0.2.0", "0.2.0", false},
		{"remote newer patch", "0.2.0", "0.2.1", true},
		{"remote newer minor", "0.2.0", "0.3.0", true},
		{"remote newer major", "0.2.0", "1.0.0", true},
		{"remote older", "0.3.0", "0.2.0", false},
		{"with v prefix", "v0.1.0", "v0.2.0", true},
		{"mixed v prefix", "0.1.0", "v0.2.0", true},
		{"dev local", "dev", "0.1.0", true},
		{"dev local with v", "dev", "v0.1.0", true},
		{"unknown local", "unknown", "0.1.0", true},
		{"empty local", "", "0.1.0", true},
		{"dev remote", "0.1.0", "dev", false},
		{"both dev", "dev", "dev", false},
		{"pre-release stripped", "0.1.0", "0.2.0-rc1", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsNewer(tt.local, tt.remote)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestClean(t *testing.T) {
	assert.Equal(t, "0.1.0", Clean("v0.1.0"))
	assert.Equal(t, "0.1.0", Clean("0.1.0"))
	assert.Equal(t, "dev", Clean("dev"))
	assert.Equal(t, "", Clean(""))
}
