package dora

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestConfigIsEnabledDefaultsTrue(t *testing.T) {
	var cfg Config
	assert.True(t, cfg.IsEnabled())
}

func TestConfigIsEnabledHonorsExplicitValue(t *testing.T) {
	disabled := false
	cfg := Config{Enabled: &disabled}
	assert.False(t, cfg.IsEnabled())

	enabled := true
	cfg.Enabled = &enabled
	assert.True(t, cfg.IsEnabled())
}
