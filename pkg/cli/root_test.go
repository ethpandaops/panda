package cli

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestUnknownCommandHint(t *testing.T) {
	hint := unknownCommandHint(errors.New(`unknown command "topic" for "panda"`))

	assert.Contains(t, hint, "most topic words are search terms")
	assert.Contains(t, hint, "panda resources")
	assert.NotContains(t, hint, "networks://active")
	assert.Contains(t, hint, "panda datasets")
	assert.Contains(t, hint, "panda search examples")
	assert.Empty(t, unknownCommandHint(errors.New("connection refused")))
}

func TestRootCommandHasVersionFlag(t *testing.T) {
	assert.Contains(t, rootCmd.Version, "commit:")
	assert.Contains(t, rootCmd.Version, "built:")
}
