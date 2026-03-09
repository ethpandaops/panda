package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/sirupsen/logrus"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/ethpandaops/mcp/pkg/auth"
	"github.com/ethpandaops/mcp/pkg/config"
	"github.com/ethpandaops/mcp/pkg/plugin"
	"github.com/ethpandaops/mcp/pkg/proxy"
	"github.com/ethpandaops/mcp/pkg/sandbox"
	"github.com/ethpandaops/mcp/pkg/types"
)

const (
	// resourceTipCacheMaxSize is the maximum number of entries in the resource tip cache.
	resourceTipCacheMaxSize = 1000
	// resourceTipCacheMaxAge is the maximum age of entries in the resource tip cache.
	resourceTipCacheMaxAge = 4 * time.Hour
)

// resourceTipCache tracks sessions that have already seen the resource tip.
// It's a bounded cache with automatic cleanup of old entries.
type resourceTipCache struct {
	mu      sync.Mutex
	entries map[string]time.Time
}

var sessionsWithResourceTip = &resourceTipCache{
	entries: make(map[string]time.Time, 64),
}

// markShown marks a session as having seen the resource tip.
// Returns true if this is the first time the session has seen the tip.
func (c *resourceTipCache) markShown(sessionKey string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Check if already shown.
	if _, exists := c.entries[sessionKey]; exists {
		return false
	}

	// Clean up old entries if cache is too large.
	if len(c.entries) >= resourceTipCacheMaxSize {
		c.cleanupLocked()
	}

	c.entries[sessionKey] = time.Now()

	return true
}

// cleanupLocked removes entries older than resourceTipCacheMaxAge.
// Must be called with mu held.
func (c *resourceTipCache) cleanupLocked() {
	cutoff := time.Now().Add(-resourceTipCacheMaxAge)

	for key, ts := range c.entries {
		if ts.Before(cutoff) {
			delete(c.entries, key)
		}
	}
}

// resourceTipMessage is shown after the first execution in a session to guide users to MCP resources.
const resourceTipMessage = `
TIP: Read mcp://getting-started for cluster rules and workflow guidance.`

const (
	// ExecutePythonToolName is the name of the execute_python tool.
	ExecutePythonToolName = "execute_python"
	// DefaultTimeout is the default execution timeout in seconds.
	DefaultTimeout = 60
	// MaxTimeout is the maximum allowed execution timeout in seconds.
	// This matches config.MaxSandboxTimeout.
	MaxTimeout = 600
	// MinTimeout is the minimum allowed execution timeout in seconds.
	MinTimeout = 1
)

// executePythonDescription is the description of the execute_python tool.
const executePythonDescription = `Execute Python code with the ethpandaops library for Ethereum data analysis.

**BEFORE YOUR FIRST QUERY:** Read mcp://getting-started for workflow guidance and critical syntax rules.

Use the search tool with ` + "`type=\"examples\"`" + ` for query patterns. Reuse session_id from responses.`

// NewExecutePythonTool creates the execute_python tool definition.
func NewExecutePythonTool(
	log logrus.FieldLogger,
	sandboxSvc sandbox.Service,
	cfg *config.Config,
	pluginReg *plugin.Registry,
	proxySvc proxy.Service,
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
		Handler: newExecutePythonHandler(log, sandboxSvc, cfg, pluginReg, proxySvc),
	}
}

// newExecutePythonHandler creates the handler function for execute_python.
func newExecutePythonHandler(
	log logrus.FieldLogger,
	sandboxSvc sandbox.Service,
	cfg *config.Config,
	pluginReg *plugin.Registry,
	proxySvc proxy.Service,
) Handler {
	handlerLog := log.WithField("tool", ExecutePythonToolName)

	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Extract code from arguments using mcp-go helper methods.
		code, err := request.RequireString("code")
		if err != nil {
			return CallToolError(fmt.Errorf("invalid arguments: %w", err)), nil
		}

		if code == "" {
			return CallToolError(fmt.Errorf("code is required")), nil
		}

		// Extract timeout from arguments using mcp-go helper with default.
		timeout := request.GetInt("timeout", cfg.Sandbox.Timeout)

		// Validate timeout.
		if timeout < MinTimeout || timeout > MaxTimeout {
			return CallToolError(
				fmt.Errorf("timeout must be between %d and %d seconds", MinTimeout, MaxTimeout),
			), nil
		}

		// Extract optional session_id.
		sessionID := request.GetString("session_id", "")

		// Extract owner ID from auth context for session binding.
		var ownerID string
		if user := auth.GetAuthUser(ctx); user != nil {
			ownerID = fmt.Sprintf("%d", user.GitHubID)
		}

		// Generate a unique execution tracking ID for token management.
		executionTrackingID := uuid.New().String()

		requestFields := logrus.Fields{
			"code_length": len(code),
			"timeout":     timeout,
			"backend":     sandboxSvc.Name(),
			"session_id":  sessionID,
			"owner_id":    ownerID,
		}
		if cfg.Sandbox.Logging.LogCode {
			requestFields["code"] = code
		}
		handlerLog.WithFields(requestFields).Info("Executing Python code")

		// Build credential-free environment variables for the sandbox.
		env, err := buildSandboxEnv(pluginReg, proxySvc)
		if err != nil {
			handlerLog.WithError(err).Error("Failed to build sandbox environment")

			return CallToolError(fmt.Errorf("failed to configure sandbox: %w", err)), nil
		}

		// Register token BEFORE execution so sandbox can use it.
		proxyToken := proxySvc.RegisterToken(executionTrackingID)
		env["ETHPANDAOPS_PROXY_TOKEN"] = proxyToken

		// Ensure token is revoked after execution completes (or fails).
		defer proxySvc.RevokeToken(executionTrackingID)

		// Check session limit before creating a new session.
		if sessionID == "" && sandboxSvc.SessionsEnabled() {
			canCreate, count, maxAllowed := sandboxSvc.CanCreateSession(ctx, ownerID)
			if !canCreate {
				return CallToolError(fmt.Errorf(
					"maximum sessions limit reached (%d/%d). Use manage_session with operation 'list' to see sessions, then 'destroy' to free up a slot",
					count, maxAllowed,
				)), nil
			}
		}

		// Execute the code in the sandbox.
		result, err := sandboxSvc.Execute(ctx, sandbox.ExecuteRequest{
			Code:      code,
			Env:       env,
			Timeout:   time.Duration(timeout) * time.Second,
			SessionID: sessionID,
			OwnerID:   ownerID,
		})
		if err != nil {
			handlerLog.WithError(err).Error("Execution failed")

			return CallToolError(fmt.Errorf("execution error: %w", err)), nil
		}

		// Format the response.
		response := formatExecutionResult(result, cfg)

		// Show resource tip after the first execution in a session.
		sessionKey := result.SessionID
		if sessionKey == "" {
			sessionKey = result.ExecutionID // Use execution ID if no session
		}

		if sessionsWithResourceTip.markShown(sessionKey) {
			response += resourceTipMessage
		}

		completionFields := logrus.Fields{
			"execution_id": result.ExecutionID,
			"exit_code":    result.ExitCode,
			"duration":     result.DurationSeconds,
			"output_files": result.OutputFiles,
			"session_id":   result.SessionID,
		}
		if cfg.Sandbox.Logging.LogOutput {
			completionFields["stdout"] = result.Stdout
			completionFields["stderr"] = result.Stderr
		}
		handlerLog.WithFields(completionFields).Info("Execution completed")

		return CallToolSuccess(response), nil
	}
}

// formatExecutionResult formats the execution result into a string.
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

	// Include session info if available with clear reuse instruction.
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

	parts = append(parts, fmt.Sprintf("[exit=%d duration=%.2fs]",
		result.ExitCode, result.DurationSeconds))

	// Add note about localhost URLs if storage is configured with localhost.
	if cfg.Storage != nil && strings.Contains(cfg.Storage.PublicURLPrefix, "localhost") {
		if strings.Contains(result.Stdout, "localhost") {
			parts = append(parts, "[note] Storage URLs use localhost - these are accessible to the user.")
		}
	}

	return strings.Join(parts, "\n")
}

// formatSize formats a byte size into a human-readable string.
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

// buildSandboxEnv creates credential-free environment variables for the sandbox.
// The proxy URL and datasource info are included, but no credentials.
// The token is added separately by the caller.
func buildSandboxEnv(
	pluginReg *plugin.Registry,
	proxySvc proxy.Service,
) (map[string]string, error) {
	// Get credential-free env vars from all plugins.
	// This includes datasource info (name, description, database/url) for each datasource type.
	env, err := pluginReg.SandboxEnv()
	if err != nil {
		return nil, fmt.Errorf("collecting sandbox env: %w", err)
	}

	// Add proxy URL.
	env["ETHPANDAOPS_PROXY_URL"] = proxySvc.URL()

	// Add S3 bucket name (no credentials).
	if bucket := proxySvc.S3Bucket(); bucket != "" {
		env["ETHPANDAOPS_S3_BUCKET"] = bucket
	}

	// Add public URL prefix for S3 if available from proxy.
	if prefix := proxySvc.S3PublicURLPrefix(); prefix != "" {
		env["ETHPANDAOPS_S3_PUBLIC_URL_PREFIX"] = prefix
	}

	// Datasources are proxy-authoritative; override any plugin-provided lists.
	delete(env, "ETHPANDAOPS_CLICKHOUSE_DATASOURCES")
	delete(env, "ETHPANDAOPS_PROMETHEUS_DATASOURCES")
	delete(env, "ETHPANDAOPS_LOKI_DATASOURCES")

	if ds := proxySvc.ClickHouseDatasourceInfo(); len(ds) > 0 {
		env["ETHPANDAOPS_CLICKHOUSE_DATASOURCES"] = buildClickhouseDatasourceJSON(ds)
	}

	if ds := proxySvc.PrometheusDatasourceInfo(); len(ds) > 0 {
		env["ETHPANDAOPS_PROMETHEUS_DATASOURCES"] = buildPrometheusDatasourceJSON(ds)
	}

	if ds := proxySvc.LokiDatasourceInfo(); len(ds) > 0 {
		env["ETHPANDAOPS_LOKI_DATASOURCES"] = buildLokiDatasourceJSON(ds)
	}

	return env, nil
}

// buildClickhouseDatasourceJSON creates a JSON array of ClickHouse datasource info objects.
func buildClickhouseDatasourceJSON(infos []types.DatasourceInfo) string {
	type dsInfo struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Database    string `json:"database"`
	}

	result := make([]dsInfo, 0, len(infos))
	for _, info := range infos {
		result = append(result, dsInfo{
			Name:        info.Name,
			Description: info.Description,
			Database:    info.Metadata["database"],
		})
	}

	return marshalDatasourceJSON(result)
}

// buildPrometheusDatasourceJSON creates a JSON array of Prometheus datasource info objects.
func buildPrometheusDatasourceJSON(infos []types.DatasourceInfo) string {
	type dsInfo struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		URL         string `json:"url"`
	}

	result := make([]dsInfo, 0, len(infos))
	for _, info := range infos {
		result = append(result, dsInfo{
			Name:        info.Name,
			Description: info.Description,
			URL:         info.Metadata["url"],
		})
	}

	return marshalDatasourceJSON(result)
}

// buildLokiDatasourceJSON creates a JSON array of Loki datasource info objects.
func buildLokiDatasourceJSON(infos []types.DatasourceInfo) string {
	type dsInfo struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		URL         string `json:"url"`
	}

	result := make([]dsInfo, 0, len(infos))
	for _, info := range infos {
		result = append(result, dsInfo{
			Name:        info.Name,
			Description: info.Description,
			URL:         info.Metadata["url"],
		})
	}

	return marshalDatasourceJSON(result)
}

func marshalDatasourceJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return "[]"
	}

	return string(data)
}
