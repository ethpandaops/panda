package cli

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestUnknownCommandHint(t *testing.T) {
	hint := unknownCommandHint(errors.New(`unknown command "topic" for "panda"`))

	assert.Contains(t, hint, "most topic words are search terms")
	// The generic unknown-command path must not steer product workflow: one
	// pointer to the next hop only.
	assert.Contains(t, hint, "panda getting-started")
	assert.NotContains(t, hint, "panda search examples")
	assert.NotContains(t, hint, "panda datasets")
	assert.Empty(t, unknownCommandHint(errors.New("connection refused")))
}

func TestRootCommandHasVersionFlag(t *testing.T) {
	assert.Contains(t, rootCmd.Version, "commit:")
	assert.Contains(t, rootCmd.Version, "built:")
}

func TestRootHelpSurfacesDiscoveryWorkflow(t *testing.T) {
	assert.Contains(t, rootCmd.Long, "panda search examples")
	assert.Contains(t, rootCmd.Long, "panda search runbooks")
	assert.Contains(t, rootCmd.Long, "panda search consensus-specs")
	assert.Contains(t, rootCmd.Long, "panda datasets")
	assert.Contains(t, rootCmd.Long, "instead of guessing")
}
