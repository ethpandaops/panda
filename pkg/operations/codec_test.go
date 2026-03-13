package operations

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testPayload struct {
	Name string `json:"name"`
}

func TestDecodeHelpersAndNewObjectResponse(t *testing.T) {
	t.Parallel()

	args, err := DecodeArgs[testPayload](map[string]any{"name": "validator"})
	require.NoError(t, err)
	assert.Equal(t, "validator", args.Name)

	value, err := DecodeValue[int](7)
	require.NoError(t, err)
	assert.Equal(t, 7, value)

	responseValue, err := DecodeResponseData[testPayload](&Response{
		Kind: ResultKindObject,
		Data: map[string]any{"name": "runbook"},
	})
	require.NoError(t, err)
	assert.Equal(t, "runbook", responseValue.Name)

	_, err = DecodeResponseData[testPayload](nil)
	require.EqualError(t, err, "operation response is required")

	_, err = DecodeArgs[testPayload](map[string]any{"name": 42})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decoding operation args")

	_, err = DecodeValue[testPayload](map[string]any{"bad": make(chan int)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "marshaling operation value")

	response := NewObjectResponse(map[string]string{"status": "ok"}, map[string]any{"source": "test"})
	assert.Equal(t, ResultKindObject, response.Kind)
	assert.Equal(t, map[string]string{"status": "ok"}, response.Data)
	assert.Equal(t, map[string]any{"source": "test"}, response.Meta)
}
