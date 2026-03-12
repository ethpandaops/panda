package cli

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func marshalJSON(t *testing.T, payload any) string {
	t.Helper()

	data, err := json.Marshal(payload)
	require.NoError(t, err)

	return string(data)
}
