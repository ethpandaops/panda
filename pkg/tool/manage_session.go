package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/auth"
	"github.com/ethpandaops/panda/pkg/execsvc"
)

const (
	// ManageSessionToolName is the name of the manage_session tool.
	ManageSessionToolName = "manage_session"
)

const manageSessionDescription = `Manage sandbox sessions. Use 'list' to see active sessions, 'create' to start a new session, or 'destroy' to remove a session.

Operations:
- list: View all active sessions with their workspace files and TTL
- create: Create a new empty session for use with execute_python
- destroy: Remove a session (requires session_id)`

// ListSessionsResponse is the response for the list operation.
type ListSessionsResponse struct {
	Sessions    []SessionDetail `json:"sessions"`
	Total       int             `json:"total"`
	MaxSessions int             `json:"max_sessions"`
}

// SessionDetail represents a session in the list response.
type SessionDetail struct {
	SessionID      string              `json:"session_id"`
	CreatedAt      string              `json:"created_at"`
	LastUsed       string              `json:"last_used"`
	TTLRemaining   string              `json:"ttl_remaining"`
	WorkspaceFiles []WorkspaceFileInfo `json:"workspace_files"`
}

// WorkspaceFileInfo represents a file in the session workspace.
type WorkspaceFileInfo struct {
	Name string `json:"name"`
	Size string `json:"size"`
}

// CreateSessionResponse is the response for the create operation.
type CreateSessionResponse struct {
	SessionID    string `json:"session_id"`
	TTLRemaining string `json:"ttl_remaining"`
	Message      string `json:"message"`
}

type manageSessionHandler struct {
	log     logrus.FieldLogger
	service *execsvc.Service
}

// NewManageSessionTool creates the manage_session tool definition.
func NewManageSessionTool(
	log logrus.FieldLogger,
	service *execsvc.Service,
) Definition {
	h := &manageSessionHandler{
		log:     log.WithField("tool", ManageSessionToolName),
		service: service,
	}

	return Definition{
		Tool: mcp.Tool{
			Name:        ManageSessionToolName,
			Description: manageSessionDescription,
			InputSchema: mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"operation": map[string]any{
						"type":        "string",
						"enum":        []string{"list", "create", "destroy"},
						"description": "The operation to perform",
					},
					"session_id": map[string]any{
						"type":        "string",
						"description": "Session ID (required for destroy operation)",
					},
				},
				Required: []string{"operation"},
			},
		},
		Handler: h.handle,
	}
}

func (h *manageSessionHandler) handle(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// Check if sessions are enabled.
	if !h.service.SessionsEnabled() {
		return CallToolError(fmt.Errorf("sessions are disabled")), nil
	}

	operation := request.GetString("operation", "")
	if operation == "" {
		return CallToolError(fmt.Errorf("operation is required")), nil
	}

	// Extract owner ID from auth context for session filtering.
	var ownerID string
	if user := auth.GetAuthUser(ctx); user != nil {
		ownerID = fmt.Sprintf("%d", user.GitHubID)
	}

	switch operation {
	case "list":
		return h.handleList(ctx, ownerID)
	case "create":
		return h.handleCreate(ctx, ownerID)
	case "destroy":
		sessionID := request.GetString("session_id", "")
		if sessionID == "" {
			return CallToolError(fmt.Errorf("session_id is required for destroy operation")), nil
		}

		return h.handleDestroy(ctx, sessionID, ownerID)
	default:
		return CallToolError(fmt.Errorf("unknown operation: %s", operation)), nil
	}
}

func (h *manageSessionHandler) handleList(ctx context.Context, ownerID string) (*mcp.CallToolResult, error) {
	h.log.WithField("owner_id", ownerID).Debug("Listing sessions")

	sessions, maxSessions, err := h.service.ListSessions(ctx, ownerID)
	if err != nil {
		return CallToolError(fmt.Errorf("listing sessions: %w", err)), nil
	}

	details := make([]SessionDetail, 0, len(sessions))
	for _, s := range sessions {
		workspaceFiles := make([]WorkspaceFileInfo, 0, len(s.WorkspaceFiles))
		for _, f := range s.WorkspaceFiles {
			workspaceFiles = append(workspaceFiles, WorkspaceFileInfo{
				Name: f.Name,
				Size: formatSize(f.Size),
			})
		}

		details = append(details, SessionDetail{
			SessionID:      s.ID,
			CreatedAt:      s.CreatedAt.Format(time.RFC3339),
			LastUsed:       s.LastUsed.Format(time.RFC3339),
			TTLRemaining:   s.TTLRemaining.Round(time.Second).String(),
			WorkspaceFiles: workspaceFiles,
		})
	}

	response := &ListSessionsResponse{
		Sessions:    details,
		Total:       len(details),
		MaxSessions: maxSessions,
	}

	data, err := json.MarshalIndent(response, "", "  ")
	if err != nil {
		return CallToolError(fmt.Errorf("marshaling response: %w", err)), nil
	}

	h.log.WithField("count", len(sessions)).Debug("Listed sessions")

	return CallToolSuccess(string(data)), nil
}

func (h *manageSessionHandler) handleCreate(ctx context.Context, ownerID string) (*mcp.CallToolResult, error) {
	h.log.WithField("owner_id", ownerID).Debug("Creating session")

	sessionID, err := h.service.CreateSession(ctx, ownerID)
	if err != nil {
		return CallToolError(err), nil
	}

	// Get TTL from listing the newly created session.
	sessions, _, err := h.service.ListSessions(ctx, ownerID)
	if err != nil {
		// Session was created but we couldn't get TTL - return with generic TTL.
		response := &CreateSessionResponse{
			SessionID:    sessionID,
			TTLRemaining: "unknown",
			Message:      "Session created. Pass this session_id to execute_python.",
		}

		data, _ := json.MarshalIndent(response, "", "  ")

		return CallToolSuccess(string(data)), nil
	}

	// Find the session we just created.
	var ttlRemaining time.Duration
	for _, s := range sessions {
		if s.ID == sessionID {
			ttlRemaining = s.TTLRemaining

			break
		}
	}

	response := &CreateSessionResponse{
		SessionID:    sessionID,
		TTLRemaining: ttlRemaining.Round(time.Second).String(),
		Message:      "Session created. Pass this session_id to execute_python.",
	}

	data, err := json.MarshalIndent(response, "", "  ")
	if err != nil {
		return CallToolError(fmt.Errorf("marshaling response: %w", err)), nil
	}

	h.log.WithField("session_id", sessionID).Info("Created session")

	return CallToolSuccess(string(data)), nil
}

func (h *manageSessionHandler) handleDestroy(
	ctx context.Context,
	sessionID, ownerID string,
) (*mcp.CallToolResult, error) {
	h.log.WithFields(logrus.Fields{
		"session_id": sessionID,
		"owner_id":   ownerID,
	}).Debug("Destroying session")

	if err := h.service.DestroySession(ctx, sessionID, ownerID); err != nil {
		return CallToolError(err), nil
	}

	h.log.WithField("session_id", sessionID).Info("Destroyed session")

	return CallToolSuccess(fmt.Sprintf("Session %s has been destroyed.", sessionID)), nil
}
