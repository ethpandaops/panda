package compute

import (
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOpenAPISpecParses ensures the embedded spec is a valid OpenAPI 3 document
// the generated client was built from.
func TestOpenAPISpecParses(t *testing.T) {
	t.Parallel()

	require.NotEmpty(t, OpenAPIYAML)

	spec, err := openapi3.NewLoader().LoadFromData(OpenAPIYAML)
	require.NoError(t, err)
	require.NoError(t, spec.Validate(t.Context()))

	assert.Equal(t, "v1", spec.Info.Version)
}

// TestOpenAPISpecHasNoUpstreamBranding guards against the upstream service name
// leaking back into the public spec when it is re-copied and regenerated. The
// compute spec must stay neutral.
func TestOpenAPISpecHasNoUpstreamBranding(t *testing.T) {
	t.Parallel()

	assert.NotContains(t, strings.ToLower(string(OpenAPIYAML)), "farplane",
		"the compute OpenAPI spec must not reference the upstream service by name")
}

// TestOpenAPISpecRoutesAreVersioned anchors the /v1 path prefix the server
// operations rely on when forwarding through the proxy.
func TestOpenAPISpecRoutesAreVersioned(t *testing.T) {
	t.Parallel()

	spec, err := openapi3.NewLoader().LoadFromData(OpenAPIYAML)
	require.NoError(t, err)

	for path := range spec.Paths.Map() {
		assert.True(t, strings.HasPrefix(path, "/v1/"),
			"compute spec path %q is not under /v1", path)
	}
}

// TestOpenAPISpecHasExpectedOperations pins the operationIds the compute server
// operations depend on. If the spec drops one on a re-copy, the generated
// client method disappears and the server stops compiling; this test surfaces
// the cause directly.
func TestOpenAPISpecHasExpectedOperations(t *testing.T) {
	t.Parallel()

	spec, err := openapi3.NewLoader().LoadFromData(OpenAPIYAML)
	require.NoError(t, err)

	want := []string{
		"listSandboxes", "createSandbox", "getSandbox", "deleteSandbox",
		"stopSandbox", "startSandbox", "snapshotSandbox", "leaseSandbox",
		"listSnapshots", "getSnapshot", "deleteSnapshot", "restoreSnapshot",
		"listTemplates", "getTemplate", "listOperations", "getOperation",
		"listSSHPublicKeys", "addSSHPublicKey", "deleteSSHPublicKey",
	}

	got := make(map[string]bool)

	for _, item := range spec.Paths.Map() {
		for _, op := range item.Operations() {
			got[op.OperationID] = true
		}
	}

	for _, id := range want {
		assert.True(t, got[id], "operationId %q missing from the compute OpenAPI spec", id)
	}
}
