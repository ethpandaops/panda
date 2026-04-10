package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/sirupsen/logrus"
)

const (
	githubAPIBase = "https://api.github.com"

	// defaultAllowedRepository is the only repository allowed by default.
	defaultAllowedRepository = "ethpandaops/eth-client-docker-image-builder"
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

	// Validate the target repository is allowed.
	if req.Repository != defaultAllowedRepository {
		writeError(w, http.StatusForbidden, "repository %q is not allowed", req.Repository)
		return
	}

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

	url := fmt.Sprintf("%s/repos/%s/actions/workflows/%s/dispatches", githubAPIBase, req.Repository, req.Workflow)

	ghReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, url, bytes.NewReader(jsonBody))
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

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(GitHubTriggerResponse{
		WorkflowURL: workflowURL,
	})
}

func writeError(w http.ResponseWriter, status int, format string, args ...any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error": fmt.Sprintf(format, args...),
	})
}
