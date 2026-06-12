package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBlockArchiveDownloadOutFlagHasNoOutputShorthand(t *testing.T) {
	flag := blockArchiveDownloadCmd.Flags().Lookup("out")
	require.NotNil(t, flag)
	assert.Empty(t, flag.Shorthand)
}
