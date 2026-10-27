package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDevnetRequestArgs(t *testing.T) {
	args, err := devnetRequestArgs(
		[]string{"glamsterdam-devnet-8", "assertoor", "post", "/api/v1/test_runs/schedule"},
		`{"test_id": "t1", "allow_duplicate": false}`,
		[]string{"limit=5", "filter=a=b"},
	)
	require.NoError(t, err)

	assert.Equal(t, map[string]any{
		"network": "glamsterdam-devnet-8",
		"service": "assertoor",
		"method":  "POST",
		"path":    "/api/v1/test_runs/schedule",
		"body":    map[string]any{"test_id": "t1", "allow_duplicate": false},
		"query":   map[string]any{"limit": "5", "filter": "a=b"},
	}, args)
}

func TestDevnetRequestArgsRejectsBadInput(t *testing.T) {
	_, err := devnetRequestArgs([]string{"n", "spamoor", "POST", "/api/spammer"}, `{"name":`, nil)
	assert.ErrorContains(t, err, "--data is not valid JSON")

	_, err = devnetRequestArgs([]string{"n", "spamoor", "GET", "/api/spammers"}, "", []string{"novalue"})
	assert.ErrorContains(t, err, "must be key=value")
}
