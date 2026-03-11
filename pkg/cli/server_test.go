package cli

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/configpath"
)

// TestResolveComposeFile is not parallel because the subtests mutate
// the package-level composeFile variable.
func TestResolveComposeFile(t *testing.T) {
	original := composeFile

	t.Cleanup(func() { composeFile = original })

	t.Run("returns flag value when set", func(t *testing.T) {
		composeFile = "/custom/path/docker-compose.yaml"

		result, err := resolveComposeFile()
		require.NoError(t, err)
		assert.Equal(t, "/custom/path/docker-compose.yaml", result)
	})

	t.Run("returns default path when flag is empty", func(t *testing.T) {
		composeFile = ""

		expected := filepath.Join(
			configpath.DefaultConfigDir(),
			"docker-compose.yaml",
		)

		result, err := resolveComposeFile()
		require.NoError(t, err)
		assert.Equal(t, expected, result)
	})
}
