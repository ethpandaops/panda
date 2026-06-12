package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/operations"
)

func TestRunSpecsConstantUsesSpecsOperation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/operations/specs.get_constant", r.URL.Path)

		var req operations.Request
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Equal(t, "MAX_EFFECTIVE_BALANCE", req.Args["name"])
		assert.Equal(t, "phase0", req.Args["fork"])

		err := json.NewEncoder(w).Encode(operations.Response{
			Kind: operations.ResultKindObject,
			Data: map[string]any{
				"name":  "MAX_EFFECTIVE_BALANCE",
				"value": "32000000000",
				"fork":  "phase0",
			},
		})
		require.NoError(t, err)
	}))
	defer server.Close()

	setClientConfig(t, server.URL)
	setOutputFormat(t, "text")
	setSpecsConstantFork(t, "phase0")

	output := captureStdout(t, func() {
		err := runSpecsConstant(testCommand(), []string{"MAX_EFFECTIVE_BALANCE"})
		require.NoError(t, err)
	})

	assert.Contains(t, output, "Name:")
	assert.Contains(t, output, "MAX_EFFECTIVE_BALANCE")
	assert.Contains(t, output, "Value:")
	assert.Contains(t, output, "32000000000")
	assert.Contains(t, output, "Fork:")
	assert.Contains(t, output, "phase0")
}

func TestRunSpecsConstantsUsesPrefixAndFork(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/operations/specs.list_constants", r.URL.Path)

		var req operations.Request
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Equal(t, "MAX_", req.Args["prefix"])
		assert.Equal(t, "deneb", req.Args["fork"])

		err := json.NewEncoder(w).Encode(operations.Response{
			Kind: operations.ResultKindObject,
			Data: map[string]any{
				"constants": []map[string]string{{
					"name":  "MAX_BLOBS_PER_BLOCK",
					"value": "6",
					"fork":  "deneb",
				}},
				"count": 1,
			},
		})
		require.NoError(t, err)
	}))
	defer server.Close()

	setClientConfig(t, server.URL)
	setOutputFormat(t, "text")
	setSpecsConstantsFlags(t, "deneb", "")

	output := captureStdout(t, func() {
		err := runSpecsConstants(testCommand(), []string{"MAX_"})
		require.NoError(t, err)
	})

	assert.Contains(t, output, "NAME")
	assert.Contains(t, output, "MAX_BLOBS_PER_BLOCK")
	assert.Contains(t, output, "deneb")
}

func TestRunSpecsDocumentUsesSpecsOperation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/operations/specs.get_spec", r.URL.Path)

		var req operations.Request
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Equal(t, "deneb", req.Args["fork"])
		assert.Equal(t, "beacon-chain", req.Args["topic"])

		err := json.NewEncoder(w).Encode(operations.Response{
			Kind: operations.ResultKindObject,
			Data: map[string]any{
				"fork":    "deneb",
				"topic":   "beacon-chain",
				"title":   "Beacon Chain",
				"url":     "https://example.test/spec",
				"content": "spec body",
			},
		})
		require.NoError(t, err)
	}))
	defer server.Close()

	setClientConfig(t, server.URL)
	setOutputFormat(t, "text")

	output := captureStdout(t, func() {
		err := runSpecsDocument(testCommand(), []string{"deneb", "beacon-chain"})
		require.NoError(t, err)
	})

	assert.Contains(t, output, "Fork:")
	assert.Contains(t, output, "deneb")
	assert.Contains(t, output, "URL:")
	assert.Contains(t, output, "https://example.test/spec")
	assert.Contains(t, output, "spec body")
}

func setSpecsConstantFork(t *testing.T, fork string) {
	t.Helper()

	original := specsConstantFork
	specsConstantFork = fork
	t.Cleanup(func() { specsConstantFork = original })
}

func setSpecsConstantsFlags(t *testing.T, fork, prefix string) {
	t.Helper()

	originalFork := specsConstantsFork
	originalPrefix := specsConstantsPref
	specsConstantsFork = fork
	specsConstantsPref = prefix
	t.Cleanup(func() {
		specsConstantsFork = originalFork
		specsConstantsPref = originalPrefix
	})
}
