package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const followSnapshotRunning = `{
  "run": {"id": "run_x", "status": "running", "outputs": {}},
  "tasks": [
    {"specNodeKey": "tasks.collect", "status": "completed"},
    {"specNodeKey": "tasks.analyze", "status": "running"}
  ]
}`

const followSnapshotTerminal = `{
  "run": {
    "id": "run_x", "status": "failed",
    "outputs": {"summary": "partial"},
    "startedAt": "2026-07-07T01:00:00Z", "finishedAt": "2026-07-07T01:02:30Z"
  },
  "tasks": [
    {"specNodeKey": "tasks.collect", "status": "completed"},
    {"specNodeKey": "tasks.analyze", "status": "failed", "error": {"message": "boom", "code": 7}}
  ],
  "resources": [
    {"id": "wart_1", "slotName": "report", "mediaType": "text/markdown", "sizeBytes": 512, "taskKey": "tasks.analyze"}
  ]
}`

func TestParseFollowViewDigestsTasksAndErrors(t *testing.T) {
	t.Parallel()

	view := parseFollowView([]byte(followSnapshotTerminal))

	assert.Equal(t, "failed", view.runStatus)
	assert.Equal(t, []string{"tasks.collect", "tasks.analyze"}, view.order)
	assert.Equal(t, "failed", view.status["tasks.analyze"])
	assert.Contains(t, view.errs["tasks.analyze"], "boom")
}

func TestPrintFollowDeltaAttachThenTransitions(t *testing.T) {
	t.Parallel()

	first := parseFollowView([]byte(followSnapshotRunning))

	var attach strings.Builder

	printFollowDelta(&attach, "run_x", followView{}, first)
	assert.Contains(t, attach.String(), "following run run_x")
	assert.Contains(t, attach.String(), "tasks.analyze  running")

	second := parseFollowView([]byte(followSnapshotTerminal))

	var delta strings.Builder

	printFollowDelta(&delta, "run_x", first, second)

	out := delta.String()
	assert.Contains(t, out, "run  running → failed")
	assert.Contains(t, out, "task tasks.analyze  running → failed")
	assert.Contains(t, out, "error:")
	assert.NotContains(t, out, "tasks.collect",
		"an unchanged task must not produce a delta line")
}

func TestPrintFollowDeltaUnchangedSnapshotIsSilent(t *testing.T) {
	t.Parallel()

	view := parseFollowView([]byte(followSnapshotRunning))

	var out strings.Builder

	printFollowDelta(&out, "run_x", view, view)
	assert.Empty(t, out.String(), "no change must print nothing")
}

func TestBuildFollowSummaryFromTerminalSnapshot(t *testing.T) {
	t.Parallel()

	summary := buildFollowSummary([]byte(followSnapshotTerminal), "wf_x", "run_x", "https://workflow.example")

	assert.Equal(t, "wf_x", summary.WorkflowID)
	assert.Equal(t, "run_x", summary.RunID)
	assert.Equal(t, "failed", summary.Status)
	assert.InDelta(t, 150.0, summary.DurationSeconds, 0.001)
	assert.Equal(t, 2, summary.TasksTotal)
	assert.Equal(t, map[string]int{"completed": 1, "failed": 1}, summary.TasksByStatus)

	require.Len(t, summary.FailedTasks, 1)
	assert.Equal(t, "tasks.analyze", summary.FailedTasks[0].ID)
	assert.Contains(t, summary.FailedTasks[0].Error, "boom")

	require.Len(t, summary.Artifacts, 1)
	assert.Equal(t, "wart_1", summary.Artifacts[0].ID)
	assert.Equal(t, "report", summary.Artifacts[0].SlotName)

	assert.Equal(t, "partial", summary.Outputs["summary"])
	assert.Equal(t, "https://workflow.example/workflows/wf_x/runs/run_x", summary.Links["run"])
	assert.Equal(t, "https://workflow.example/workflows/wf_x", summary.Links["workflow"])
}

func TestBuildFollowSummaryWithoutWebBaseHasNoLinks(t *testing.T) {
	t.Parallel()

	summary := buildFollowSummary([]byte(followSnapshotTerminal), "wf_x", "run_x", "")
	assert.Nil(t, summary.Links)
}

func TestBuildFollowSummaryUndecodableSnapshotKeepsIDs(t *testing.T) {
	t.Parallel()

	summary := buildFollowSummary([]byte("not json"), "wf_x", "run_x", "")

	assert.Equal(t, "wf_x", summary.WorkflowID)
	assert.Empty(t, summary.Status)
}

func TestCompactFollowErrorCapsAndFlattens(t *testing.T) {
	t.Parallel()

	assert.Empty(t, compactFollowError(nil))
	assert.Equal(t, "plain text", compactFollowError("plain\n text"))

	long := strings.Repeat("x", followTaskErrorCap+50)
	capped := compactFollowError(long)
	assert.Len(t, capped, followTaskErrorCap+len("…"))
}

func TestFollowDurationSeconds(t *testing.T) {
	t.Parallel()

	assert.Zero(t, followDurationSeconds("", "2026-07-07T01:00:00Z"))
	assert.Zero(t, followDurationSeconds("bad", "worse"))
	assert.InDelta(t, 90.0,
		followDurationSeconds("2026-07-07T01:00:00Z", "2026-07-07T01:01:30Z"), 0.001)
}
