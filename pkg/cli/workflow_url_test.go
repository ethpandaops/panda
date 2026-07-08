package cli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildWorkflowWebURLForms(t *testing.T) {
	t.Parallel()

	base := "https://workflow.example"

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "bare origin", args: nil, want: base},
		{name: "whiteboard", args: []string{"whiteboard", "wb_1"}, want: base + "/whiteboards/wb_1"},
		{name: "workflow", args: []string{"workflow", "wf_1"}, want: base + "/workflows/wf_1"},
		{name: "run", args: []string{"run", "wf_1", "run_1"}, want: base + "/workflows/wf_1/runs/run_1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := buildWorkflowWebURL(base, tt.args)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestBuildWorkflowWebURLRejectsUnknownForms(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"whiteboard"},
		{"run", "wf_1"},
		{"bogus", "x"},
	} {
		_, err := buildWorkflowWebURL("https://workflow.example", args)
		require.Error(t, err, "args %v must be rejected", args)
		assert.Contains(t, err.Error(), "usage:")
	}
}

// workflowInfoServer serves a fixed status+body on /api/v1/workflow-info and
// records the request path. Not parallel-safe with other client-config tests:
// setClientConfig mutates package globals.
func workflowInfoServer(t *testing.T, status int, body string) (baseURL string, gotPath *string) {
	t.Helper()

	var path string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)

	return server.URL, &path
}

func TestFetchWorkflowWebBaseFromWorkflowInfo(t *testing.T) {
	serverURL, gotPath := workflowInfoServer(t, http.StatusOK,
		`{"enabled":true,"web_base_url":"https://workflow.example/"}`)
	setClientConfig(t, serverURL)

	base, err := fetchWorkflowWebBase(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "/api/v1/workflow-info", *gotPath)
	// The trailing slash is trimmed so path joins don't double it.
	assert.Equal(t, "https://workflow.example", base)
}

func TestFetchWorkflowWebBaseDegradesToEmpty(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "engine not advertised", status: http.StatusOK, body: `{}`},
		{name: "disabled with stale url", status: http.StatusOK, body: `{"enabled":false,"web_base_url":"https://stale.example"}`},
		{name: "server predates workflow-info", status: http.StatusNotFound, body: "404 page not found"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			serverURL, _ := workflowInfoServer(t, tt.status, tt.body)
			setClientConfig(t, serverURL)

			base, err := fetchWorkflowWebBase(context.Background())
			require.NoError(t, err)
			assert.Empty(t, base)
		})
	}
}

func TestFetchWorkflowWebBaseSurfacesErrors(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{
			name:   "engine not advertised on any proxy",
			status: http.StatusServiceUnavailable,
			body:   `{"detail":"workflow engine is not available: no configured proxy advertises it"}`,
			want:   "not enabled on any configured proxy",
		},
		{
			name:   "malformed payload",
			status: http.StatusOK,
			body:   `{"enabled":`,
			want:   "parsing workflow-info",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			serverURL, _ := workflowInfoServer(t, tt.status, tt.body)
			setClientConfig(t, serverURL)

			_, err := fetchWorkflowWebBase(context.Background())
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)

			// The best-effort variant swallows the same failure into link-less output.
			assert.Empty(t, workflowWebBaseBestEffort(context.Background()))
		})
	}
}

func TestWorkflowURLCommandBuildsLinkFromInfo(t *testing.T) {
	serverURL, _ := workflowInfoServer(t, http.StatusOK,
		`{"enabled":true,"web_base_url":"https://workflow.example"}`)
	setClientConfig(t, serverURL)
	setOutputFormat(t, "text")

	output := captureStdout(t, func() {
		require.NoError(t, workflowURLCmd.RunE(testCommand(), []string{"run", "wf_1", "run_1"}))
	})

	assert.Equal(t, "https://workflow.example/workflows/wf_1/runs/run_1\n", output)
}

func TestWorkflowURLCommandErrorsWhenNotAdvertised(t *testing.T) {
	serverURL, _ := workflowInfoServer(t, http.StatusOK, `{}`)
	setClientConfig(t, serverURL)

	err := workflowURLCmd.RunE(testCommand(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not advertised by any configured proxy")
}
