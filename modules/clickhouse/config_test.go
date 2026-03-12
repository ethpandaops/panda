package clickhouse

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSchemaDiscoveryConfigIsEnabledDefaultsTrue(t *testing.T) {
	var cfg SchemaDiscoveryConfig
	assert.True(t, cfg.IsEnabled())
}

func TestSchemaDiscoveryConfigIsEnabledHonorsExplicitValue(t *testing.T) {
	disabled := false
	cfg := SchemaDiscoveryConfig{Enabled: &disabled}
	assert.False(t, cfg.IsEnabled())

	enabled := true
	cfg.Enabled = &enabled
	assert.True(t, cfg.IsEnabled())
}
