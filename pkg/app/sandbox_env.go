package app

import (
	"fmt"
	"strings"
)

// BuildSandboxEnv assembles the environment shared by sandbox executions and sessions.
func (a *App) BuildSandboxEnv() (map[string]string, error) {
	if a.ModuleRegistry == nil {
		return nil, fmt.Errorf("module registry is not initialized")
	}

	env, err := a.ModuleRegistry.SandboxEnv()
	if err != nil {
		return nil, fmt.Errorf("collecting sandbox env: %w", err)
	}
	if env == nil {
		env = make(map[string]string, 1)
	}

	apiURL := a.sandboxAPIURL()
	if apiURL == "" {
		return nil, fmt.Errorf("server.sandbox_url or server.base_url is required for sandbox API access")
	}

	env["ETHPANDAOPS_API_URL"] = apiURL

	return env, nil
}

func (a *App) sandboxAPIURL() string {
	if a == nil || a.cfg == nil {
		return ""
	}

	if value := strings.TrimSpace(a.cfg.Server.SandboxURL); value != "" {
		return strings.TrimRight(value, "/")
	}

	if value := strings.TrimSpace(a.cfg.Server.BaseURL); value != "" {
		return strings.TrimRight(value, "/")
	}

	if value := strings.TrimSpace(a.cfg.Server.URL); value != "" {
		return strings.TrimRight(value, "/")
	}

	port := a.cfg.Server.Port
	if port == 0 {
		port = 2480
	}
	return fmt.Sprintf("http://host.docker.internal:%d", port)
}
