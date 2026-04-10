package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

const (
	githubAPIBase = "https://api.github.com"

	// defaultAllowedRepository is the only repository allowed by default.
	defaultAllowedRepository = "ethpandaops/eth-client-docker-image-builder"

	// runFindTimeout is how long to poll for the triggered run after dispatch.
	runFindTimeout = 20 * time.Second
	// runFindInterval is how often to poll.
	runFindInterval = 2 * time.Second
)

// GitHubConfig holds GitHub API proxy configuration.
type GitHubConfig struct {
	Token string
}

// GitHubTriggerRequest is the request body for triggering a workflow.
type GitHubTriggerRequest struct {
	// Repository is the target GitHub repository (e.g. "ethpandaops/eth-client-docker-image-builder").
	Repository string `json:"repository"`
	// Workflow is the workflow filename (e.g. "build-push-geth.yml").
	Workflow string `json:"workflow"`
	// Ref is the git ref to run the workflow on (typically "master").
	Ref string `json:"ref"`
	// Inputs are the workflow_dispatch inputs.
	Inputs map[string]string `json:"inputs,omitempty"`
}

// GitHubTriggerResponse is the response from a successful workflow trigger.
type GitHubTriggerResponse struct {
	WorkflowURL string `json:"workflow_url"`
	RunID       int64  `json:"run_id,omitempty"`
	RunURL      string `json:"run_url,omitempty"`
}

// GitHubRunStatusRequest is the request for checking a run's status.
type GitHubRunStatusRequest struct {
	Repository string `json:"repository"`
	RunID      int64  `json:"run_id"`
}

// GitHubRunStatusResponse is the response from a run status check.
type GitHubRunStatusResponse struct {
	RunID      int64  `json:"run_id"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	HTMLURL    string `json:"html_url"`
}

// gitHubWorkflowRun is a subset of the GitHub Actions run object.
type gitHubWorkflowRun struct {
	ID         int64  `json:"id"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	HTMLURL    string `json:"html_url"`
	CreatedAt  string `json:"created_at"`
}

// GitHubHandler handles GitHub API requests.
type GitHubHandler struct {
	log        logrus.FieldLogger
	token      string
	httpClient *http.Client
}

// NewGitHubHandler creates a new GitHub handler.
func NewGitHubHandler(log logrus.FieldLogger, cfg GitHubConfig) *GitHubHandler {
	return &GitHubHandler{
		log:        log.WithField("handler", "github"),
		token:      cfg.Token,
		httpClient: &http.Client{},
	}
}

// ServeHTTP routes GitHub API requests.
func (h *GitHubHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/github")

	switch {
	case path == "/actions/trigger" && r.Method == http.MethodPost:
		h.handleTriggerWorkflow(w, r)
	case path == "/actions/run-status" && r.Method == http.MethodPost:
		h.handleRunStatus(w, r)
	default:
		http.Error(w, fmt.Sprintf("unknown github endpoint: %s %s", r.Method, path), http.StatusNotFound)
	}
}

func (h *GitHubHandler) handleTriggerWorkflow(w http.ResponseWriter, r *http.Request) {
	var req GitHubTriggerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: %v", err)
		return
	}

	if req.Repository == "" {
		writeError(w, http.StatusBadRequest, "repository is required")
		return
	}

	if req.Workflow == "" {
		writeError(w, http.StatusBadRequest, "workflow is required")
		return
	}

	if req.Ref == "" {
		req.Ref = "master"
	}

	if req.Repository != defaultAllowedRepository {
		writeError(w, http.StatusForbidden, "repository %q is not allowed", req.Repository)
		return
	}

	// Record time before dispatch so we can find the run.
	dispatchTime := time.Now().UTC()

	// Build GitHub API request.
	body := map[string]any{
		"ref":    req.Ref,
		"inputs": req.Inputs,
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to marshal request: %v", err)
		return
	}

	dispatchURL := fmt.Sprintf("%s/repos/%s/actions/workflows/%s/dispatches", githubAPIBase, req.Repository, req.Workflow)

	ghReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, dispatchURL, bytes.NewReader(jsonBody))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create request: %v", err)
		return
	}

	ghReq.Header.Set("Accept", "application/vnd.github.v3+json")
	ghReq.Header.Set("Authorization", "Bearer "+h.token)
	ghReq.Header.Set("Content-Type", "application/json")

	resp, err := h.httpClient.Do(ghReq)
	if err != nil {
		writeError(w, http.StatusBadGateway, "github request failed: %v", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(resp.Body)

		h.log.WithFields(logrus.Fields{
			"status":     resp.StatusCode,
			"repository": req.Repository,
			"workflow":   req.Workflow,
			"response":   string(respBody),
		}).Error("GitHub workflow dispatch failed")

		writeError(w, http.StatusBadGateway, "github returned status %d: %s", resp.StatusCode, string(respBody))

		return
	}

	h.log.WithFields(logrus.Fields{
		"repository": req.Repository,
		"workflow":   req.Workflow,
		"ref":        req.Ref,
		"inputs":     req.Inputs,
	}).Info("Triggered GitHub workflow")

	workflowURL := fmt.Sprintf("https://github.com/%s/actions/workflows/%s", req.Repository, req.Workflow)

	triggerResp := GitHubTriggerResponse{
		WorkflowURL: workflowURL,
	}

	// Try to find the workflow run that was just triggered.
	if run := h.findTriggeredRun(r.Context(), req.Repository, req.Workflow, dispatchTime); run != nil {
		triggerResp.RunID = run.ID
		triggerResp.RunURL = run.HTMLURL
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(triggerResp)
}

func (h *GitHubHandler) handleRunStatus(w http.ResponseWriter, r *http.Request) {
	var req GitHubRunStatusRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: %v", err)
		return
	}

	if req.Repository == "" || req.RunID == 0 {
		writeError(w, http.StatusBadRequest, "repository and run_id are required")
		return
	}

	if req.Repository != defaultAllowedRepository {
		writeError(w, http.StatusForbidden, "repository %q is not allowed", req.Repository)
		return
	}

	run, err := h.getRun(r.Context(), req.Repository, req.RunID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to get run status: %v", err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(GitHubRunStatusResponse{
		RunID:      run.ID,
		Status:     run.Status,
		Conclusion: run.Conclusion,
		HTMLURL:    run.HTMLURL,
	})
}

// findTriggeredRun polls GitHub to find the workflow run created after dispatchTime.
func (h *GitHubHandler) findTriggeredRun(ctx context.Context, repo, workflow string, after time.Time) *gitHubWorkflowRun {
	deadline := time.After(runFindTimeout)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-deadline:
			h.log.Warn("Timed out finding triggered workflow run")
			return nil
		default:
		}

		url := fmt.Sprintf(
			"%s/repos/%s/actions/workflows/%s/runs?event=workflow_dispatch&per_page=5&created=>=%%3A%s",
			githubAPIBase, repo, workflow, after.Format("2006-01-02T15:04:05Z"),
		)

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil
		}

		req.Header.Set("Accept", "application/vnd.github.v3+json")
		req.Header.Set("Authorization", "Bearer "+h.token)

		resp, err := h.httpClient.Do(req)
		if err != nil {
			return nil
		}

		var result struct {
			WorkflowRuns []gitHubWorkflowRun `json:"workflow_runs"`
		}

		err = json.NewDecoder(resp.Body).Decode(&result)
		_ = resp.Body.Close()

		if err != nil {
			return nil
		}

		if len(result.WorkflowRuns) > 0 {
			run := &result.WorkflowRuns[0]

			h.log.WithFields(logrus.Fields{
				"run_id": run.ID,
				"status": run.Status,
				"url":    run.HTMLURL,
			}).Info("Found triggered workflow run")

			return run
		}

		time.Sleep(runFindInterval)
	}
}

// getRun fetches a specific workflow run by ID.
func (h *GitHubHandler) getRun(ctx context.Context, repo string, runID int64) (*gitHubWorkflowRun, error) {
	url := fmt.Sprintf("%s/repos/%s/actions/runs/%d", githubAPIBase, repo, runID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("Authorization", "Bearer "+h.token)

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("github returned %d: %s", resp.StatusCode, string(body))
	}

	var run gitHubWorkflowRun
	if err := json.NewDecoder(resp.Body).Decode(&run); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	return &run, nil
}

func writeError(w http.ResponseWriter, status int, format string, args ...any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error": fmt.Sprintf(format, args...),
	})
}
