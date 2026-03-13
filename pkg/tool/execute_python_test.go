package tool

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/config"
	"github.com/ethpandaops/panda/pkg/execsvc"
	"github.com/ethpandaops/panda/pkg/sandbox"
)

func TestFormatExecutionResultIncludesSessionState(t *testing.T) {
	t.Parallel()

	result := &sandbox.ExecutionResult{
		Stdout:              "hello",
		ExitCode:            0,
		DurationSeconds:     1.25,
		SessionID:           "session-1",
		SessionTTLRemaining: 91*time.Second + 400*time.Millisecond,
		SessionFiles: []sandbox.SessionFile{
			{Name: "notes.txt", Size: 1536},
			{Name: "script.py", Size: 12},
		},
	}

	formatted := formatExecutionResult(result, &config.Config{})

	assert.True(t, strings.Contains(formatted, "[stdout]\nhello"))
	assert.True(t, strings.Contains(formatted, "[session] id=session-1 ttl=1m31s"))
	assert.True(t, strings.Contains(formatted, "notes.txt(1.5 KB)"))
	assert.True(t, strings.Contains(formatted, "script.py(12 B)"))
	assert.True(t, strings.Contains(formatted, "[exit=0 duration=1.25s]"))
}

func TestResourceTipCacheMarksOnceAndExpiresByClock(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.March, 11, 16, 45, 0, 0, time.UTC)
	cache := &resourceTipCache{
		now:     func() time.Time { return now },
		entries: make(map[string]time.Time),
	}

	assert.True(t, cache.markShown("session-1"))
	assert.False(t, cache.markShown("session-1"))

	now = now.Add(resourceTipCacheMaxAge + time.Minute)
	cache.cleanupLocked()
	assert.True(t, cache.markShown("session-2"))
	assert.NotContains(t, cache.entries, "session-1")
}

func TestExecutePythonHandlerEmitsResourceTipOncePerSession(t *testing.T) {
	t.Parallel()

	service := &stubExecutePythonService{
		results: []*sandbox.ExecutionResult{
			{ExecutionID: "exec-1", SessionID: "session-1", ExitCode: 0, DurationSeconds: 0.5},
			{ExecutionID: "exec-2", SessionID: "session-1", ExitCode: 0, DurationSeconds: 0.5},
		},
	}
	cache := &resourceTipCache{
		now:     func() time.Time { return time.Date(2026, time.March, 11, 17, 0, 0, 0, time.UTC) },
		entries: make(map[string]time.Time),
	}
	handler := newExecutePythonHandler(
		logrus.New(),
		"docker",
		&config.Config{Sandbox: config.SandboxConfig{Timeout: DefaultTimeout}},
		service,
		cache,
	)

	request := executePythonCall("print('hello')")

	firstResult, err := handler(context.Background(), request)
	require.NoError(t, err)
	require.False(t, firstResult.IsError)

	secondResult, err := handler(context.Background(), request)
	require.NoError(t, err)
	require.False(t, secondResult.IsError)

	assert.Contains(t, resultText(t, firstResult), resourceTipMessage)
	assert.NotContains(t, resultText(t, secondResult), resourceTipMessage)
}

func TestExecutePythonHandlerUsesExecutionIDWhenSessionIDIsAbsent(t *testing.T) {
	t.Parallel()

	service := &stubExecutePythonService{
		results: []*sandbox.ExecutionResult{
			{ExecutionID: "exec-1", ExitCode: 0, DurationSeconds: 0.25},
			{ExecutionID: "exec-1", ExitCode: 0, DurationSeconds: 0.25},
		},
	}
	cache := &resourceTipCache{
		now:     func() time.Time { return time.Date(2026, time.March, 11, 17, 5, 0, 0, time.UTC) },
		entries: make(map[string]time.Time),
	}
	handler := newExecutePythonHandler(
		logrus.New(),
		"docker",
		&config.Config{Sandbox: config.SandboxConfig{Timeout: DefaultTimeout}},
		service,
		cache,
	)

	request := executePythonCall("print('hello')")

	firstResult, err := handler(context.Background(), request)
	require.NoError(t, err)
	secondResult, err := handler(context.Background(), request)
	require.NoError(t, err)

	assert.Contains(t, resultText(t, firstResult), resourceTipMessage)
	assert.NotContains(t, resultText(t, secondResult), resourceTipMessage)
}

func executePythonCall(code string) mcp.CallToolRequest {
	return mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]any{
				"code": code,
			},
		},
	}
}

func resultText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()

	require.Len(t, result.Content, 1)
	text, ok := result.Content[0].(mcp.TextContent)
	require.True(t, ok)

	return text.Text
}

type stubExecutePythonService struct {
	results []*sandbox.ExecutionResult
	err     error
	calls   int
}

func (s *stubExecutePythonService) Execute(context.Context, execsvc.ExecuteRequest) (*sandbox.ExecutionResult, error) {
	if s.err != nil {
		return nil, s.err
	}

	if s.calls >= len(s.results) {
		return nil, nil
	}

	result := s.results[s.calls]
	s.calls++

	return result, nil
}
