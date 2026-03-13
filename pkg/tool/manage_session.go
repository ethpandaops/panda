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
	"github.com/ethpandaops/panda/pkg/sandbox"
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

// ManageSessionResponse is the structured response envelope for every manage_session operation.
type ManageSessionResponse struct {
	Operation   string                 `json:"operation"`
	Sessions    []SessionDetail        `json:"sessions"`
	Session     *ManagedSessionSummary `json:"session,omitempty"`
	Total       int                    `json:"total"`
	MaxSessions int                    `json:"max_sessions"`
	Message     string                 `json:"message,omitempty"`
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

// ManagedSessionSummary is the session payload used across create and destroy responses.
type ManagedSessionSummary struct {
	SessionID    string `json:"session_id"`
	TTLRemaining string `json:"ttl_remaining,omitempty"`
}

type manageSessionService interface {
	SessionsEnabled() bool
	ListSessions(ctx context.Context, ownerID string) ([]sandbox.SessionInfo, int, error)
	CreateSession(ctx context.Context, ownerID string) (*sandbox.CreatedSession, error)
	DestroySession(ctx context.Context, sessionID, ownerID string) error
}

type manageSessionHandler struct {
	log     logrus.FieldLogger
	service manageSessionService
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
	sessions, maxSessions, err := h.service.ListSessions(ctx, ownerID)
	if err != nil {
		return CallToolError(fmt.Errorf("listing sessions: %w", err)), nil
	}

	return marshalManageSessionResponse(buildListSessionsResponse(sessions, maxSessions))
}

func (h *manageSessionHandler) handleCreate(ctx context.Context, ownerID string) (*mcp.CallToolResult, error) {
	created, err := h.service.CreateSession(ctx, ownerID)
	if err != nil {
		return CallToolError(err), nil
	}

	h.log.WithField("session_id", created.ID).Info("Created session")

	return marshalManageSessionResponse(buildCreateSessionResponse(created))
}

func (h *manageSessionHandler) handleDestroy(
	ctx context.Context,
	sessionID, ownerID string,
) (*mcp.CallToolResult, error) {
	if err := h.service.DestroySession(ctx, sessionID, ownerID); err != nil {
		return CallToolError(err), nil
	}

	h.log.WithField("session_id", sessionID).Info("Destroyed session")

	return marshalManageSessionResponse(buildDestroySessionResponse(sessionID))
}

func buildListSessionsResponse(sessions []sandbox.SessionInfo, maxSessions int) *ManageSessionResponse {
	details := make([]SessionDetail, 0, len(sessions))
	for _, session := range sessions {
		details = append(details, sessionDetailFromInfo(session))
	}

	return &ManageSessionResponse{
		Operation:   "list",
		Sessions:    details,
		Total:       len(details),
		MaxSessions: maxSessions,
	}
}

func sessionDetailFromInfo(session sandbox.SessionInfo) SessionDetail {
	workspaceFiles := make([]WorkspaceFileInfo, 0, len(session.WorkspaceFiles))
	for _, file := range session.WorkspaceFiles {
		workspaceFiles = append(workspaceFiles, WorkspaceFileInfo{
			Name: file.Name,
			Size: formatSize(file.Size),
		})
	}

	return SessionDetail{
		SessionID:      session.ID,
		CreatedAt:      session.CreatedAt.Format(time.RFC3339),
		LastUsed:       session.LastUsed.Format(time.RFC3339),
		TTLRemaining:   session.TTLRemaining.Round(time.Second).String(),
		WorkspaceFiles: workspaceFiles,
	}
}

func buildCreateSessionResponse(created *sandbox.CreatedSession) *ManageSessionResponse {
	response := &ManageSessionResponse{
		Operation: "create",
		Session: &ManagedSessionSummary{
			SessionID: created.ID,
		},
		Message: "Session created. Pass this session_id to execute_python.",
	}

	if created.TTLRemaining > 0 {
		response.Session.TTLRemaining = created.TTLRemaining.Round(time.Second).String()
	}

	return response
}

func buildDestroySessionResponse(sessionID string) *ManageSessionResponse {
	return &ManageSessionResponse{
		Operation: "destroy",
		Session: &ManagedSessionSummary{
			SessionID: sessionID,
		},
		Message: fmt.Sprintf("Session %s has been destroyed.", sessionID),
	}
}

func marshalManageSessionResponse(response *ManageSessionResponse) (*mcp.CallToolResult, error) {
	data, err := json.MarshalIndent(response, "", "  ")
	if err != nil {
		return CallToolError(fmt.Errorf("marshaling response: %w", err)), nil
	}

	return CallToolSuccess(string(data)), nil
}

var _ manageSessionService = (*execsvc.Service)(nil)
