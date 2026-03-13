package configutil

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubstituteEnvVarsReplacesValuesAndDefaults(t *testing.T) {
	t.Setenv("PANDA_URL", "https://server.example")
	t.Setenv("EMPTY_VALUE", "")

	content := strings.Join([]string{
		"server_url: ${PANDA_URL}",
		"fallback: ${MISSING_VAR:-default-value}",
		"empty_uses_default: ${EMPTY_VALUE:-fallback}",
		"# comment: ${PANDA_URL}",
		"literal: keep",
	}, "\n")

	substituted, err := SubstituteEnvVars(content)
	require.NoError(t, err)
	assert.Contains(t, substituted, "server_url: https://server.example")
	assert.Contains(t, substituted, "fallback: default-value")
	assert.Contains(t, substituted, "empty_uses_default: fallback")
	assert.Contains(t, substituted, "# comment: ${PANDA_URL}")
	assert.Contains(t, substituted, "literal: keep")
}
