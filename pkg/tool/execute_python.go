package tool

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/ethpandaops/panda/pkg/auth"
	"github.com/ethpandaops/panda/pkg/config"
	"github.com/ethpandaops/panda/pkg/execsvc"
	"github.com/ethpandaops/panda/pkg/sandbox"
)

const (
	resourceTipCacheMaxSize = 1000
	resourceTipCacheMaxAge  = 4 * time.Hour
)

type resourceTipCache struct {
	mu      sync.Mutex
	now     func() time.Time
	entries map[string]time.Time
}

var sessionsWithResourceTip = &resourceTipCache{
	now:     time.Now,
	entries: make(map[string]time.Time, 64),
}

func (c *resourceTipCache) markShown(sessionKey string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.entries[sessionKey]; exists {
		return false
	}

	if len(c.entries) >= resourceTipCacheMaxSize {
		c.cleanupLocked()
	}

	c.entries[sessionKey] = c.now()

	return true
}

func (c *resourceTipCache) cleanupLocked() {
	cutoff := c.now().Add(-resourceTipCacheMaxAge)

	for key, ts := range c.entries {
		if ts.Before(cutoff) {
			delete(c.entries, key)
		}
	}
}

const resourceTipMessage = `
TIP: Read panda://getting-started for cluster rules and workflow guidance.`

const (
	ExecutePythonToolName = "execute_python"
	DefaultTimeout        = 60
	MaxTimeout            = execsvc.MaxTimeout
	MinTimeout            = execsvc.MinTimeout
)

const executePythonDescription = `Execute Python code with the ethpandaops library for Ethereum data analysis.

**BEFORE YOUR FIRST QUERY:** Read panda://getting-started for workflow guidance and critical syntax rules.

Use the search tool with ` + "`type=\"examples\"`" + ` for query patterns. Reuse session_id from responses.`

type executePythonService interface {
	Execute(ctx context.Context, req execsvc.ExecuteRequest) (*sandbox.ExecutionResult, error)
}

type executePythonHandler struct {
	log         logrus.FieldLogger
	backendName string
	cfg         *config.Config
	service     executePythonService
	tipCache    *resourceTipCache
}

func NewExecutePythonTool(
	log logrus.FieldLogger,
	sandboxSvc sandbox.Service,
	cfg *config.Config,
	service *execsvc.Service,
) Definition {
	return Definition{
		Tool: mcp.Tool{
			Name:        ExecutePythonToolName,
			Description: executePythonDescription,
			InputSchema: mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"code": map[string]any{
						"type":        "string",
						"description": "Python code to execute",
					},
					"timeout": map[string]any{
						"type":        "integer",
						"description": "Execution timeout in seconds (default: from config, max: 600)",
						"minimum":     MinTimeout,
						"maximum":     MaxTimeout,
					},
					"session_id": map[string]any{
						"type":        "string",
						"description": "Session ID from a previous call. ALWAYS pass this when available - it preserves files and is faster. Only omit on the very first call.",
					},
				},
				Required: []string{"code"},
			},
		},
		Handler: newExecutePythonHandler(log, sandboxSvc.Name(), cfg, service, sessionsWithResourceTip),
	}
}

func newExecutePythonHandler(
	log logrus.FieldLogger,
	backendName string,
	cfg *config.Config,
	service executePythonService,
	tipCache *resourceTipCache,
) Handler {
	handler := &executePythonHandler{
		log:         log.WithField("tool", ExecutePythonToolName),
		backendName: backendName,
		cfg:         cfg,
		service:     service,
		tipCache:    tipCache,
	}

	return handler.handle
}

func (h *executePythonHandler) handle(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	code, err := request.RequireString("code")
	if err != nil {
		return CallToolError(fmt.Errorf("invalid arguments: %w", err)), nil
	}

	if code == "" {
		return CallToolError(fmt.Errorf("code is required")), nil
	}

	timeout := request.GetInt("timeout", h.cfg.Sandbox.Timeout)
	if timeout < MinTimeout || timeout > MaxTimeout {
		return CallToolError(fmt.Errorf("timeout must be between %d and %d seconds", MinTimeout, MaxTimeout)), nil
	}

	sessionID := request.GetString("session_id", "")

	var ownerID string
	if user := auth.GetAuthUser(ctx); user != nil {
		ownerID = fmt.Sprintf("%d", user.GitHubID)
	}

	requestFields := logrus.Fields{
		"code_length": len(code),
		"timeout":     timeout,
		"backend":     h.backendName,
		"session_id":  sessionID,
		"owner_id":    ownerID,
	}
	if h.cfg.Sandbox.Logging.LogCode {
		requestFields["code"] = code
	}
	h.log.WithFields(requestFields).Info("Executing Python code")

	result, err := h.service.Execute(ctx, execsvc.ExecuteRequest{
		Code:      code,
		Timeout:   timeout,
		SessionID: sessionID,
		OwnerID:   ownerID,
	})
	if err != nil {
		h.log.WithError(err).Error("Execution failed")
		return CallToolError(fmt.Errorf("execution error: %w", err)), nil
	}

	response := h.buildResponse(result)

	completionFields := logrus.Fields{
		"execution_id": result.ExecutionID,
		"exit_code":    result.ExitCode,
		"duration":     result.DurationSeconds,
		"output_files": result.OutputFiles,
		"session_id":   result.SessionID,
	}
	if h.cfg.Sandbox.Logging.LogOutput {
		completionFields["stdout"] = result.Stdout
		completionFields["stderr"] = result.Stderr
	}
	h.log.WithFields(completionFields).Info("Execution completed")

	return CallToolSuccess(response), nil
}

func (h *executePythonHandler) buildResponse(result *sandbox.ExecutionResult) string {
	response := formatExecutionResult(result, h.cfg)

	if h.tipCache == nil {
		return response
	}

	if h.tipCache.markShown(resultSessionKey(result)) {
		return response + resourceTipMessage
	}

	return response
}

func resultSessionKey(result *sandbox.ExecutionResult) string {
	if result.SessionID != "" {
		return result.SessionID
	}

	return result.ExecutionID
}

func formatExecutionResult(result *sandbox.ExecutionResult, cfg *config.Config) string {
	var parts []string

	if result.Stdout != "" {
		parts = append(parts, fmt.Sprintf("[stdout]\n%s", result.Stdout))
	}

	if result.Stderr != "" {
		parts = append(parts, fmt.Sprintf("[stderr]\n%s", result.Stderr))
	}

	if len(result.OutputFiles) > 0 {
		parts = append(parts, fmt.Sprintf("[files] %s", strings.Join(result.OutputFiles, ", ")))
	}

	if result.SessionID != "" {
		sessionInfo := fmt.Sprintf("[session] id=%s ttl=%s → REUSE THIS session_id IN ALL SUBSEQUENT CALLS",
			result.SessionID, result.SessionTTLRemaining.Round(time.Second))

		if len(result.SessionFiles) > 0 {
			workspaceFiles := make([]string, 0, len(result.SessionFiles))
			for _, f := range result.SessionFiles {
				workspaceFiles = append(workspaceFiles, fmt.Sprintf("%s(%s)", f.Name, formatSize(f.Size)))
			}

			sessionInfo += fmt.Sprintf(" workspace=[%s]", strings.Join(workspaceFiles, ", "))
		}

		parts = append(parts, sessionInfo)
	}

	parts = append(parts, fmt.Sprintf("[exit=%d duration=%.2fs]", result.ExitCode, result.DurationSeconds))

	return strings.Join(parts, "\n")
}

func formatSize(bytes int64) string {
	const unit = 1024

	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}

	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}

	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}
