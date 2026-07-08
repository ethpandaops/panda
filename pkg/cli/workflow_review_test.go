package cli

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sampleDraftBody builds a realistic draft object: graph nodes out of
// dependency order, a compiledSpecJson embedded as a JSON string, and the
// provided-inputs override at top-level .inputs (which must be ignored).
func sampleDraftBody(t *testing.T) []byte {
	t.Helper()

	compiled := map[string]any{
		"inputs": map[string]any{
			"values": map[string]any{
				"network": map[string]any{
					"description": "network to query",
					"schema":      map[string]any{"type": "string", "default": "mainnet"},
				},
				"hours": map[string]any{
					"required": true,
					"schema":   map[string]any{"type": "number", "default": 24},
				},
			},
			"secrets": map[string]any{
				"api_token": map[string]any{},
			},
		},
		"outputs": map[string]any{
			"values": map[string]any{
				"summary": map[string]any{"schema": map[string]any{"type": "string"}},
			},
			"artifacts": map[string]any{
				"report": map[string]any{"contentType": "text/markdown"},
			},
		},
	}

	compiledJSON, err := json.Marshal(compiled)
	require.NoError(t, err)

	draft := map[string]any{
		"id":       "dr_test",
		"revision": 2,
		"status":   "candidate",
		"inputs":   map[string]any{},
		"graph": map[string]any{
			"nodes": []map[string]any{
				{"id": "tasks.report", "name": "report", "kind": "task", "needs": []string{"analyze"}},
				{"id": "tasks.collect", "name": "collect", "kind": "task"},
				{
					"id": "tasks.analyze", "name": "analyze", "kind": "task",
					"needs":       []string{"tasks.collect"},
					"qualityGate": true, "hasRetry": true,
				},
			},
		},
		"compiledSpecJson": string(compiledJSON),
	}

	body, err := json.Marshal(draft)
	require.NoError(t, err)

	return body
}

func TestBuildDraftReviewParsesIdentityPortsAndDAG(t *testing.T) {
	t.Parallel()

	review, err := buildDraftReview(sampleDraftBody(t))
	require.NoError(t, err)

	assert.Equal(t, "dr_test", review.ID)
	assert.Equal(t, "candidate", review.Status)
	assert.Empty(t, review.SpecNote)

	// Inputs sorted by name; defaults surfaced from schema.default.
	require.Len(t, review.Inputs.Values, 2)
	assert.Equal(t, "hours", review.Inputs.Values[0].Name)
	assert.True(t, review.Inputs.Values[0].Required)
	assert.True(t, review.Inputs.Values[0].HasDefault)
	assert.Equal(t, "network", review.Inputs.Values[1].Name)
	assert.Equal(t, "mainnet", review.Inputs.Values[1].Default)
	assert.Equal(t, "network to query", review.Inputs.Values[1].Description)

	require.Len(t, review.Inputs.Secrets, 1)
	assert.Equal(t, "api_token", review.Inputs.Secrets[0].Name)

	require.Len(t, review.Outputs.Artifacts, 1)
	assert.Equal(t, "report", review.Outputs.Artifacts[0].Name)
	assert.Equal(t, "text/markdown", review.Outputs.Artifacts[0].ContentType)

	// DAG in dependency order, resolving needs by full id AND short name.
	require.Len(t, review.DAG, 3)
	assert.Equal(t, "tasks.collect", review.DAG[0].ID)
	assert.Equal(t, "tasks.analyze", review.DAG[1].ID)
	assert.True(t, review.DAG[1].QualityGate)
	assert.True(t, review.DAG[1].HasRetry)
	assert.Equal(t, "tasks.report", review.DAG[2].ID)
}

func TestBuildDraftReviewDegradesWithoutCompiledSpec(t *testing.T) {
	t.Parallel()

	review, err := buildDraftReview([]byte(`{"id":"dr_x","graph":{"nodes":[{"id":"tasks.a"}]}}`))
	require.NoError(t, err)

	assert.NotEmpty(t, review.SpecNote, "missing compiledSpecJson must be flagged, not silent")
	assert.Empty(t, review.Inputs.Values)
	require.Len(t, review.DAG, 1)
}

func TestBuildDraftReviewRejectsNonDraft(t *testing.T) {
	t.Parallel()

	_, err := buildDraftReview([]byte(`{"items":[]}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no draft id")
}

func TestBuildDraftReviewStringQualityGate(t *testing.T) {
	t.Parallel()

	body := []byte(`{"id":"dr_x","graph":{"nodes":[
		{"id":"tasks.a","qualityGate":"outputs.values.ok == true"},
		{"id":"tasks.b","qualityGate":""}
	]}}`)

	review, err := buildDraftReview(body)
	require.NoError(t, err)

	assert.True(t, review.DAG[0].QualityGate, "CEL-string gate must read as set")
	assert.False(t, review.DAG[1].QualityGate, "empty string must read as unset")
}

func TestTopoSortReviewNodesCycleFallsBackToInputOrder(t *testing.T) {
	t.Parallel()

	nodes := []draftReviewNode{
		{ID: "tasks.a", Needs: []string{"tasks.b"}},
		{ID: "tasks.b", Needs: []string{"tasks.a"}},
		{ID: "tasks.c"},
	}

	out := topoSortReviewNodes(nodes)
	require.Len(t, out, 3, "a cycle must not drop nodes")
	assert.Equal(t, "tasks.c", out[0].ID, "the acyclic node still sorts first")
}

func TestTopoSortReviewNodesDanglingNeedIsIgnored(t *testing.T) {
	t.Parallel()

	nodes := []draftReviewNode{
		{ID: "tasks.a", Needs: []string{"tasks.gone"}},
	}

	out := topoSortReviewNodes(nodes)
	require.Len(t, out, 1, "a need pointing outside the graph must not wedge the sort")
}

func TestRequireDraftApproval(t *testing.T) {
	t.Parallel()

	t.Run("missing flag refuses with review instructions", func(t *testing.T) {
		t.Parallel()

		err := requireDraftApproval("", "dr_x")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "side-effect boundary")
		assert.Contains(t, err.Error(), "draft show")
		assert.Contains(t, err.Error(), "--approved dr_x")
	})

	t.Run("stale draft id refuses", func(t *testing.T) {
		t.Parallel()

		err := requireDraftApproval("dr_old", "dr_new")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "does not match")
	})

	t.Run("matching draft id passes", func(t *testing.T) {
		t.Parallel()

		require.NoError(t, requireDraftApproval("dr_x", "dr_x"))
	})
}

func TestRequireRunApproval(t *testing.T) {
	t.Parallel()

	t.Run("missing flag refuses and steers to the fresh path", func(t *testing.T) {
		t.Parallel()

		err := requireRunApproval("", "wf_x")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "side-effect boundary")
		assert.Contains(t, err.Error(), "Reuse is not the default")
		assert.Contains(t, err.Error(), "workflow list")
		assert.Contains(t, err.Error(), "--approved wf_x")
	})

	t.Run("mismatched workflow id refuses", func(t *testing.T) {
		t.Parallel()

		err := requireRunApproval("wf_other", "wf_x")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "does not match")
	})

	t.Run("matching workflow id passes", func(t *testing.T) {
		t.Parallel()

		require.NoError(t, requireRunApproval("wf_x", "wf_x"))
	})
}
