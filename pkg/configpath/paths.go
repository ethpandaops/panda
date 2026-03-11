package configpath

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type NotFoundError struct {
	Kind       string
	Searched   []string
	Suggestion string
}

func (e *NotFoundError) Error() string {
	if len(e.Searched) == 0 {
		return fmt.Sprintf("no %s found. %s", e.Kind, e.Suggestion)
	}

	return fmt.Sprintf(
		"no %s found. looked in: %s. %s",
		e.Kind,
		strings.Join(e.Searched, ", "),
		e.Suggestion,
	)
}

func DefaultConfigDir() string {
	if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" {
		return filepath.Join(xdg, "panda")
	}

	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".config", "panda")
	}

	return filepath.Join(home, ".config", "panda")
}

func DefaultAppConfigPath() string {
	return filepath.Join(DefaultConfigDir(), "config.yaml")
}

func DefaultProxyConfigPath() string {
	return filepath.Join(DefaultConfigDir(), "proxy-config.yaml")
}

func ResolveAppConfigPath(explicit string) (string, error) {
	if explicit != "" {
		return filepath.Clean(explicit), nil
	}

	for _, envVar := range []string{"PANDA_CONFIG", "ETHPANDAOPS_CONFIG", "CONFIG_PATH"} {
		if value := strings.TrimSpace(os.Getenv(envVar)); value != "" {
			return filepath.Clean(value), nil
		}
	}

	candidates := []string{
		DefaultAppConfigPath(),
		"config.yaml",
	}

	if resolved, ok := firstExisting(candidates); ok {
		return resolved, nil
	}

	return "", &NotFoundError{
		Kind:       "panda config",
		Searched:   dedupe(candidates),
		Suggestion: fmt.Sprintf("Run `panda init` to create %s, or pass --config.", DefaultAppConfigPath()),
	}
}

func ResolveProxyConfigPath(explicit, baseDir string) (string, error) {
	if explicit != "" {
		return cleanRelative(baseDir, explicit), nil
	}

	for _, envVar := range []string{"PANDA_PROXY_CONFIG", "ETHPANDAOPS_PROXY_CONFIG", "CONFIG_PATH"} {
		if value := strings.TrimSpace(os.Getenv(envVar)); value != "" {
			return filepath.Clean(value), nil
		}
	}

	candidates := make([]string, 0, 3)
	if baseDir != "" {
		candidates = append(candidates, filepath.Join(baseDir, "proxy-config.yaml"))
	}

	candidates = append(candidates,
		DefaultProxyConfigPath(),
		"proxy-config.yaml",
	)

	if resolved, ok := firstExisting(candidates); ok {
		return resolved, nil
	}

	return "", &NotFoundError{
		Kind:       "panda proxy config",
		Searched:   dedupe(candidates),
		Suggestion: fmt.Sprintf("Create %s, or pass --config.", DefaultProxyConfigPath()),
	}
}

func cleanRelative(baseDir, value string) string {
	if filepath.IsAbs(value) || baseDir == "" {
		return filepath.Clean(value)
	}

	return filepath.Clean(filepath.Join(baseDir, value))
}

func firstExisting(paths []string) (string, bool) {
	for _, candidate := range dedupe(paths) {
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() {
			return candidate, true
		}
	}

	return "", false
}

func dedupe(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	result := make([]string, 0, len(paths))

	for _, candidate := range paths {
		if candidate == "" {
			continue
		}

		candidate = filepath.Clean(candidate)
		if _, ok := seen[candidate]; ok {
			continue
		}

		seen[candidate] = struct{}{}
		result = append(result, candidate)
	}

	return result
}
