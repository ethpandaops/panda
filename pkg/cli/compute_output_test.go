package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderComputeRawListIsTableByDefault(t *testing.T) {
	setOutputFormat(t, "text")

	body := []byte(`{"items":[{"id":"sbx-1","status":"running","template_version":"ubuntu/24.04"},` +
		`{"id":"sbx-2","status":"stopped","template_version":"ubuntu/24.04"}],"total":2}`)

	output := captureStdout(t, func() {
		require.NoError(t, renderComputeRaw("compute.list_sandboxes", body))
	})

	assert.Contains(t, output, "ID")
	assert.Contains(t, output, "STATUS")
	assert.Contains(t, output, "sbx-1")
	assert.Contains(t, output, "running")
	assert.Contains(t, output, "2 results.")
	assert.NotContains(t, output, "{")
}

func TestRenderComputeRawJSONPassthrough(t *testing.T) {
	setOutputFormat(t, "json")

	body := []byte(`{"items":[{"id":"sbx-1"}],"total":1}`)

	output := captureStdout(t, func() {
		require.NoError(t, renderComputeRaw("compute.list_sandboxes", body))
	})

	assert.Contains(t, output, `"items"`)
	assert.Contains(t, output, "sbx-1")
}

func TestRenderComputeRawObjectIsKeyValue(t *testing.T) {
	setOutputFormat(t, "text")

	body := []byte(`{"id":"sbx-1","status":"running","node":"node-a"}`)

	output := captureStdout(t, func() {
		require.NoError(t, renderComputeRaw("compute.get_sandbox", body))
	})

	assert.Contains(t, output, "id:")
	assert.Contains(t, output, "sbx-1")
	assert.Contains(t, output, "status:")
	assert.Contains(t, output, "running")
}

func TestRenderComputeRawEmptyListMessage(t *testing.T) {
	setOutputFormat(t, "text")

	output := captureStdout(t, func() {
		require.NoError(t, renderComputeRaw("compute.list_sandboxes", []byte(`{"items":[],"total":0}`)))
	})

	assert.Contains(t, output, "No results found.")
}

func TestRenderComputeRawAcceptedShowsPollHint(t *testing.T) {
	setOutputFormat(t, "text")

	body := []byte(`{"op_id":"op-123","sandbox_id":"sbx-9"}`)

	output := captureStdout(t, func() {
		require.NoError(t, renderComputeRaw("compute.create_sandbox", body))
	})

	assert.Contains(t, output, "Accepted.")
	assert.Contains(t, output, "op-123")
	assert.Contains(t, output, "panda compute operations get op-123")
}

func TestComputeListArgsForwardsFilters(t *testing.T) {
	original := computeFilters
	t.Cleanup(func() { computeFilters = original })

	computeFilters = []string{"status=running", "vcpu>=4"}
	args := computeListArgs()

	assert.Equal(t, []string{"status=running", "vcpu>=4"}, args["filter"])
}

func TestComputeListArgsOmitsEmptyFilters(t *testing.T) {
	original := computeFilters
	t.Cleanup(func() { computeFilters = original })

	computeFilters = nil
	args := computeListArgs()

	_, present := args["filter"]
	assert.False(t, present)
}
