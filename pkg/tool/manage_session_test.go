package tool

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/sandbox"
)

func TestManageSessionHandleCreateUsesCreatedSessionResult(t *testing.T) {
	t.Parallel()

	service := &stubManageSessionService{
		created: &sandbox.CreatedSession{
			ID:           "session-1",
			TTLRemaining: 15 * time.Minute,
		},
	}
	handler := &manageSessionHandler{
		log:     logrus.New(),
		service: service,
	}

	result, err := handler.handle(context.Background(), callToolRequest("create", ""))
	require.NoError(t, err)
	require.False(t, result.IsError)
	assert.False(t, service.listCalled)

	response := decodeManageSessionResponse(t, result)
	assert.Equal(t, "create", response.Operation)
	require.NotNil(t, response.Session)
	assert.Equal(t, "session-1", response.Session.SessionID)
	assert.Equal(t, (15 * time.Minute).Round(time.Second).String(), response.Session.TTLRemaining)
}

func TestManageSessionHandleListAndDestroyReturnConsistentEnvelope(t *testing.T) {
	t.Parallel()

	service := &stubManageSessionService{
		sessions: []sandbox.SessionInfo{
			{
				ID:           "session-1",
				CreatedAt:    time.Unix(100, 0).UTC(),
				LastUsed:     time.Unix(200, 0).UTC(),
				TTLRemaining: 5 * time.Minute,
			},
		},
		maxSessions: 3,
	}
	handler := &manageSessionHandler{
		log:     logrus.New(),
		service: service,
	}

	listResult, err := handler.handle(context.Background(), callToolRequest("list", ""))
	require.NoError(t, err)
	require.False(t, listResult.IsError)

	listResponse := decodeManageSessionResponse(t, listResult)
	assert.Equal(t, "list", listResponse.Operation)
	assert.Len(t, listResponse.Sessions, 1)
	assert.Equal(t, 1, listResponse.Total)
	assert.Equal(t, 3, listResponse.MaxSessions)

	destroyResult, err := handler.handle(context.Background(), callToolRequest("destroy", "session-1"))
	require.NoError(t, err)
	require.False(t, destroyResult.IsError)

	destroyResponse := decodeManageSessionResponse(t, destroyResult)
	assert.Equal(t, "destroy", destroyResponse.Operation)
	require.NotNil(t, destroyResponse.Session)
	assert.Equal(t, "session-1", destroyResponse.Session.SessionID)
	assert.Equal(t, "session-1", service.destroyedSessionID)
}

func callToolRequest(operation, sessionID string) mcp.CallToolRequest {
	args := map[string]any{
		"operation": operation,
	}
	if sessionID != "" {
		args["session_id"] = sessionID
	}

	return mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: args,
		},
	}
}

func decodeManageSessionResponse(t *testing.T, result *mcp.CallToolResult) ManageSessionResponse {
	t.Helper()

	require.Len(t, result.Content, 1)
	text, ok := result.Content[0].(mcp.TextContent)
	require.True(t, ok)

	var response ManageSessionResponse
	require.NoError(t, json.Unmarshal([]byte(text.Text), &response))

	return response
}

type stubManageSessionService struct {
	sessions           []sandbox.SessionInfo
	maxSessions        int
	created            *sandbox.CreatedSession
	listCalled         bool
	destroyedSessionID string
}

func (s *stubManageSessionService) SessionsEnabled() bool { return true }

func (s *stubManageSessionService) ListSessions(context.Context, string) ([]sandbox.SessionInfo, int, error) {
	s.listCalled = true
	return s.sessions, s.maxSessions, nil
}

func (s *stubManageSessionService) CreateSession(context.Context, string) (*sandbox.CreatedSession, error) {
	return s.created, nil
}

func (s *stubManageSessionService) DestroySession(_ context.Context, sessionID, _ string) error {
	s.destroyedSessionID = sessionID
	return nil
}
