package cli

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/ethpandaops/panda/pkg/config"
	"github.com/ethpandaops/panda/pkg/configpath"
)

// paramType describes how a config parameter is edited.
type paramType int

const (
	paramString         paramType = iota // free-text input
	paramOptionalString                  // free-text input (empty allowed)
	paramInt                             // integer input
	paramFloat                           // float input
	paramBool                            // toggle
	paramDuration                        // Go duration string (e.g. "30m", "4h")
	paramPort                            // port number (1-65535)
)

// configParam describes a single configurable setting.
type configParam struct {
	Name        string
	Description string
	Path        string // dot-separated YAML path for the override map
	Type        paramType
	Value       string // current value as a string (editable)
	Original    string // value at load time (for change detection)
	Default     any    // base default for override map (omit if unchanged)
}

// configCategory groups related config parameters.
type configCategory struct {
	Name        string
	Description string
	Params      []*configParam
}

var configTUICmd = &cobra.Command{
	GroupID: groupSetup,
	Use:     "config",
	Short:   "Configure panda settings",
	Long: `Open an interactive editor to configure panda settings.

Changes are saved to config.user.yaml and survive 'panda init' and 'panda upgrade'.
The server must be restarted for changes to take effect.`,
	RunE: runConfigTUI,
}

func init() {
	rootCmd.AddCommand(configTUICmd)
}

func runConfigTUI(_ *cobra.Command, _ []string) error {
	resolvedPath, err := configpath.ResolveAppConfigPath(cfgFile)
	if err != nil {
		return fmt.Errorf("no config found — run 'panda init' first: %w", err)
	}

	userPath := config.UserConfigPath(resolvedPath)

	cfg, err := config.LoadWithUserOverrides(resolvedPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	existingOverrides, err := config.LoadUserConfigMap(userPath)
	if err != nil {
		return fmt.Errorf("loading user overrides: %w", err)
	}

	categories := buildCategories(cfg)

	display := newConfigDisplay(categories, resolvedPath, userPath, existingOverrides)
	display.run()

	if !display.saved {
		fmt.Println("No changes saved.")
		return nil
	}

	fmt.Printf("\nConfiguration saved to %s\n", userPath)

	// Offer to restart the server.
	compose := resolveComposeFile()
	if _, err := os.Stat(compose); os.IsNotExist(err) {
		fmt.Println("Run 'panda server restart' to apply changes.")
		return nil
	}

	if promptConfirm("Restart server to apply changes?") {
		fmt.Println("Restarting server...")

		return runDockerCompose(compose, "restart")
	}

	fmt.Println("Run 'panda server restart' to apply changes.")

	return nil
}

// buildCategories creates the configurable parameter categories from the current config.
func buildCategories(cfg *config.Config) []configCategory {
	return []configCategory{
		{
			Name:        "Sandbox Execution",
			Description: "Configure execution limits for the Python sandbox, including timeout, memory, and CPU constraints.",
			Params: []*configParam{
				{
					Name:        "Timeout (seconds)",
					Description: "Maximum time in seconds allowed for a single Python execution.\n\nSet this based on your workload — short for interactive queries, longer for heavy analysis.",
					Path:        "sandbox.timeout",
					Type:        paramInt,
					Value:       strconv.Itoa(cfg.Sandbox.Timeout),
					Default:     60,
				},
				{
					Name:        "Memory Limit",
					Description: "Maximum memory available to the sandbox container.\n\nExamples: 512m, 1g, 2g, 4g, 8g, 16g",
					Path:        "sandbox.memory_limit",
					Type:        paramString,
					Value:       cfg.Sandbox.MemoryLimit,
					Default:     "2g",
				},
				{
					Name:        "CPU Limit",
					Description: "Maximum CPU cores available to the sandbox container.\n\nExamples: 0.5, 1.0, 2.0, 4.0",
					Path:        "sandbox.cpu_limit",
					Type:        paramFloat,
					Value:       formatFloat(cfg.Sandbox.CPULimit),
					Default:     1.0,
				},
			},
		},
		{
			Name:        "Sessions",
			Description: "Configure persistent sandbox sessions. Sessions allow code to share state across multiple executions.",
			Params: []*configParam{
				{
					Name:        "Sessions Enabled",
					Description: "When enabled, sandbox sessions persist state across multiple Python executions.\n\nDisable if you want every execution to start fresh.",
					Path:        "sandbox.sessions.enabled",
					Type:        paramBool,
					Value:       strconv.FormatBool(cfg.Sandbox.Sessions.IsEnabled()),
					Default:     true,
				},
				{
					Name:        "Max Concurrent Sessions",
					Description: "Maximum number of sessions that can exist simultaneously.\n\nHigher values use more resources but allow more parallel workstreams.",
					Path:        "sandbox.sessions.max_sessions",
					Type:        paramInt,
					Value:       strconv.Itoa(cfg.Sandbox.Sessions.MaxSessions),
					Default:     10,
				},
				{
					Name:        "Session Idle Timeout",
					Description: "Destroy a session after this period of inactivity.\n\nExamples: 30m, 1h, 4h, 10h, 24h",
					Path:        "sandbox.sessions.ttl",
					Type:        paramDuration,
					Value:       formatDuration(cfg.Sandbox.Sessions.TTL),
					Default:     "30m0s",
				},
				{
					Name:        "Session Max Lifetime",
					Description: "Absolute maximum lifetime for a session regardless of activity.\n\nExamples: 4h, 24h, 72h, 168h (1 week)",
					Path:        "sandbox.sessions.max_duration",
					Type:        paramDuration,
					Value:       formatDuration(cfg.Sandbox.Sessions.MaxDuration),
					Default:     "4h0m0s",
				},
			},
		},
		{
			Name:        "Sandbox Logging",
			Description: "Control what sandbox activity is logged. Useful for debugging but may expose sensitive data.",
			Params: []*configParam{
				{
					Name:        "Log Submitted Code",
					Description: "Log the full Python code submitted to execute_python.\n\nUseful for debugging, but code may contain sensitive data.",
					Path:        "sandbox.logging.log_code",
					Type:        paramBool,
					Value:       strconv.FormatBool(cfg.Sandbox.Logging.LogCode),
					Default:     false,
				},
				{
					Name:        "Log Execution Output",
					Description: "Log stdout and stderr from sandbox executions.\n\nOutput can be large or contain sensitive data.",
					Path:        "sandbox.logging.log_output",
					Type:        paramBool,
					Value:       strconv.FormatBool(cfg.Sandbox.Logging.LogOutput),
					Default:     false,
				},
			},
		},
		{
			Name:        "Server",
			Description: "Configure the panda server, proxy connection, and observability settings.",
			Params: []*configParam{
				{
					Name:        "Server Port",
					Description: "Port the panda server listens on.\n\nDefault: 2480. Changing this also requires updating your docker-compose port mapping.",
					Path:        "server.port",
					Type:        paramPort,
					Value:       strconv.Itoa(cfg.Server.Port),
					Default:     2480,
				},
				{
					Name:        "Proxy URL",
					Description: "URL of the credential proxy server.\n\nThis is where the server sends credentialed upstream requests.",
					Path:        "proxy.url",
					Type:        paramString,
					Value:       cfg.Proxy.URL,
					Default:     "http://localhost:18081",
				},
				{
					Name:        "Metrics Port",
					Description: "Port for the Prometheus metrics endpoint.\n\nDefault: 2490.",
					Path:        "observability.metrics_port",
					Type:        paramPort,
					Value:       strconv.Itoa(cfg.Observability.MetricsPort),
					Default:     2490,
				},
			},
		},
		{
			Name:        "Modules: Consensus Specs",
			Description: "Configure how ethereum/consensus-specs are fetched from GitHub.\n\nSpec documents and protocol constants are indexed for semantic search and available in Python via ethpandaops.specs.",
			Params: []*configParam{
				{
					Name:        "Repository",
					Description: "GitHub owner/repo to fetch consensus specs from.\n\nChange this to point at a fork, e.g. \"myorg/consensus-specs\".\n\nDefault: ethereum/consensus-specs",
					Path:        "consensus_specs.repository",
					Type:        paramString,
					Value:       cfg.ConsensusSpecs.Repository,
					Default:     "ethereum/consensus-specs",
				},
				{
					Name:        "Ref",
					Description: "Git ref (branch, tag, or commit SHA) to fetch.\n\nLeave empty to track the latest GitHub release automatically.\n\nExamples: dev, v1.5.0-alpha.10, electra",
					Path:        "consensus_specs.ref",
					Type:        paramOptionalString,
					Value:       cfg.ConsensusSpecs.Ref,
					Default:     "",
				},
			},
		},
	}
}

// snapshotOriginals saves the current Value as Original for all params (for change detection).
func snapshotOriginals(categories []configCategory) {
	for i := range categories {
		for j := range categories[i].Params {
			categories[i].Params[j].Original = categories[i].Params[j].Value
		}
	}
}

// buildOverrideMap creates the override map from current param values.
func buildOverrideMap(categories []configCategory, existing map[string]any) (map[string]any, error) {
	var fields []config.OverrideField

	for _, cat := range categories {
		for _, p := range cat.Params {
			val, err := parseParamValue(p)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", p.Name, err)
			}

			defaultVal := p.Default

			// If the user actively changed this param, always include it in the
			// override map — even if the new value matches the base default.
			// This ensures "reset to default" correctly overrides a previously
			// saved user value in config.user.yaml.
			if p.Value != p.Original {
				defaultVal = nil
			}

			fields = append(fields, config.OverrideField{
				Path:    p.Path,
				Value:   val,
				Default: defaultVal,
			})
		}
	}

	overrides := config.BuildOverrideMap(fields)

	// Deep-merge onto a copy of existing overrides to preserve fields not in the TUI.
	return config.DeepMerge(existing, overrides), nil
}

// parseParamValue converts a param's string Value to its typed form.
func parseParamValue(p *configParam) (any, error) {
	switch p.Type {
	case paramInt:
		return strconv.Atoi(p.Value)
	case paramFloat:
		return strconv.ParseFloat(p.Value, 64)
	case paramBool:
		return strconv.ParseBool(p.Value)
	case paramPort:
		n, err := strconv.Atoi(p.Value)
		if err != nil {
			return nil, err
		}

		if n < 1 || n > 65535 {
			return nil, fmt.Errorf("port must be between 1 and 65535")
		}

		return n, nil
	case paramDuration:
		if _, err := time.ParseDuration(p.Value); err != nil {
			return nil, err
		}

		return p.Value, nil
	default:
		return p.Value, nil
	}
}

// validateParamValue returns an error message if the value is invalid for the
// given param type, or empty string if valid.
func validateParamValue(pt paramType, value string) string {
	switch pt {
	case paramInt:
		if _, err := strconv.Atoi(value); err != nil || value == "" {
			return "Must be a number"
		}
	case paramFloat:
		if _, err := strconv.ParseFloat(value, 64); err != nil || value == "" {
			return "Must be a number"
		}
	case paramPort:
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 65535 {
			return "Must be 1-65535"
		}
	case paramDuration:
		if _, err := time.ParseDuration(value); err != nil {
			return "Invalid duration (e.g. 30m, 2h, 24h)"
		}
	case paramString:
		if value == "" {
			return "Cannot be empty"
		}
	}

	return ""
}

// changedParams returns all params whose Value differs from Original.
func changedParams(categories []configCategory) []*configParam {
	var changed []*configParam

	for _, cat := range categories {
		for _, p := range cat.Params {
			if p.Value != p.Original {
				changed = append(changed, p)
			}
		}
	}

	return changed
}

// formatDuration formats a time.Duration as a human-friendly string.
func formatDuration(d time.Duration) string {
	if d == 0 {
		return "0s"
	}

	hours := int(d.Hours())
	minutes := int(d.Minutes()) % 60

	switch {
	case hours > 0 && minutes == 0:
		return fmt.Sprintf("%dh", hours)
	case hours > 0:
		return fmt.Sprintf("%dh%dm", hours, minutes)
	default:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
}

// formatFloat formats a float64 without trailing zeros.
func formatFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}
