package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ethpandaops/panda/pkg/configpath"
)

const (
	// UserConfigFilename is the name of the user override config file.
	UserConfigFilename = "config.user.yaml"

	// userConfigHeader is written at the top of user config files.
	userConfigHeader = "# User configuration overrides for panda.\n# Managed by 'panda config'. Survives 'panda init' and 'panda upgrade'.\n"
)

// UserConfigPath returns the path to the user config override file,
// derived as a sibling of the given main config path.
func UserConfigPath(mainConfigPath string) string {
	return filepath.Join(filepath.Dir(mainConfigPath), UserConfigFilename)
}

// DefaultUserConfigPath returns the user config path in the default config directory.
func DefaultUserConfigPath() string {
	return filepath.Join(configpath.DefaultConfigDir(), UserConfigFilename)
}

// LoadWithUserOverrides loads the base config and merges any user overrides
// from config.user.yaml found alongside the main config file.
func LoadWithUserOverrides(path string) (*Config, error) {
	resolvedPath, err := configpath.ResolveAppConfigPath(path)
	if err != nil {
		return nil, err
	}

	// Read and substitute base config.
	baseData, err := os.ReadFile(resolvedPath)
	if err != nil {
		return nil, fmt.Errorf("reading config file %s: %w", resolvedPath, err)
	}

	baseSubstituted, err := substituteEnvVars(string(baseData))
	if err != nil {
		return nil, fmt.Errorf("substituting env vars in base config: %w", err)
	}

	// Parse base config as a map for merging.
	var baseMap map[string]any
	if err := yaml.Unmarshal([]byte(baseSubstituted), &baseMap); err != nil {
		return nil, fmt.Errorf("parsing base config as map: %w", err)
	}

	if baseMap == nil {
		baseMap = make(map[string]any, 8)
	}

	// Merge user override file if it exists.
	userPath := UserConfigPath(resolvedPath)

	userData, err := os.ReadFile(userPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("reading user config %s: %w", userPath, err)
	}

	if err == nil {
		userSubstituted, subErr := substituteEnvVars(string(userData))
		if subErr != nil {
			return nil, fmt.Errorf("substituting env vars in user config: %w", subErr)
		}

		var userMap map[string]any
		if err := yaml.Unmarshal([]byte(userSubstituted), &userMap); err != nil {
			return nil, fmt.Errorf("parsing user config: %w", err)
		}

		if len(userMap) > 0 {
			baseMap = DeepMerge(baseMap, userMap)
		}
	}

	// Marshal merged map back to YAML and decode into Config struct with strict parsing.
	mergedYAML, err := yaml.Marshal(baseMap)
	if err != nil {
		return nil, fmt.Errorf("marshaling merged config: %w", err)
	}

	var cfg Config
	decoder := yaml.NewDecoder(bytes.NewReader(mergedYAML))
	decoder.KnownFields(true)

	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parsing merged config: %w", err)
	}

	applyDefaults(&cfg)

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validating merged config: %w", err)
	}

	cfg.path = resolvedPath

	return &cfg, nil
}

// ValidateMergedConfig validates that applying overrides to the base config
// at the given path produces a valid configuration. This is a pure in-memory
// check — no files are modified.
func ValidateMergedConfig(basePath string, overrides map[string]any) error {
	resolvedPath, err := configpath.ResolveAppConfigPath(basePath)
	if err != nil {
		return err
	}

	baseData, err := os.ReadFile(resolvedPath)
	if err != nil {
		return fmt.Errorf("reading config: %w", err)
	}

	baseSub, err := substituteEnvVars(string(baseData))
	if err != nil {
		return err
	}

	var baseMap map[string]any
	if err := yaml.Unmarshal([]byte(baseSub), &baseMap); err != nil {
		return fmt.Errorf("parsing config: %w", err)
	}

	if baseMap == nil {
		baseMap = make(map[string]any, 8)
	}

	merged := DeepMerge(baseMap, overrides)

	mergedYAML, err := yaml.Marshal(merged)
	if err != nil {
		return fmt.Errorf("marshaling merged config: %w", err)
	}

	var cfg Config
	decoder := yaml.NewDecoder(bytes.NewReader(mergedYAML))
	decoder.KnownFields(true)

	if err := decoder.Decode(&cfg); err != nil {
		return fmt.Errorf("parsing merged config: %w", err)
	}

	applyDefaults(&cfg)

	return cfg.Validate()
}

// SaveUserConfig writes user override values to the given path.
// Values should be a nested map matching the config YAML structure,
// containing only the fields the user has overridden.
func SaveUserConfig(path string, values map[string]any) error {
	data, err := yaml.Marshal(values)
	if err != nil {
		return fmt.Errorf("marshaling user config: %w", err)
	}

	content := userConfigHeader + string(data)

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("writing user config: %w", err)
	}

	return nil
}

// LoadUserConfigMap loads the user config file as a raw map.
// Returns an empty map if the file does not exist.
func LoadUserConfigMap(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return make(map[string]any, 8), nil
		}

		return nil, fmt.Errorf("reading user config: %w", err)
	}

	content := string(data)

	substituted, err := substituteEnvVars(content)
	if err != nil {
		return nil, fmt.Errorf("substituting env vars: %w", err)
	}

	var m map[string]any
	if err := yaml.Unmarshal([]byte(substituted), &m); err != nil {
		return nil, fmt.Errorf("parsing user config: %w", err)
	}

	if m == nil {
		return make(map[string]any, 8), nil
	}

	return m, nil
}

// UserConfigPlaceholder returns the content for an empty user config placeholder file.
func UserConfigPlaceholder() string {
	return userConfigHeader
}

// deepMerge recursively merges overlay into base. Overlay values win for leaf
// values; nested maps are merged recursively. Neither input map is modified.
func DeepMerge(base, overlay map[string]any) map[string]any {
	result := make(map[string]any, len(base))

	for k, v := range base {
		result[k] = v
	}

	for k, ov := range overlay {
		bv, exists := result[k]
		if !exists {
			result[k] = ov

			continue
		}

		// If both values are maps, merge recursively.
		bm, bIsMap := toStringMap(bv)
		om, oIsMap := toStringMap(ov)

		if bIsMap && oIsMap {
			result[k] = DeepMerge(bm, om)
		} else {
			result[k] = ov
		}
	}

	return result
}

// toStringMap attempts to convert a value to map[string]any.
// YAML unmarshaling produces map[string]any by default.
func toStringMap(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok
}

// setNestedValue sets a value in a nested map structure, creating intermediate
// maps as needed. Keys represent the path (e.g., ["sandbox", "timeout"]).
func setNestedValue(m map[string]any, keys []string, value any) {
	for i, key := range keys {
		if i == len(keys)-1 {
			m[key] = value

			return
		}

		next, ok := m[key].(map[string]any)
		if !ok {
			next = make(map[string]any, 4)
			m[key] = next
		}

		m = next
	}
}

// BuildOverrideMap constructs a minimal override map from field descriptors.
// Only fields where the current value differs from the base default are included.
func BuildOverrideMap(fields []OverrideField) map[string]any {
	result := make(map[string]any, 8)

	for _, f := range fields {
		if f.Value != f.Default {
			setNestedValue(result, strings.Split(f.Path, "."), f.Value)
		}
	}

	return result
}

// OverrideField describes a single configurable field for building override maps.
type OverrideField struct {
	// Path is the dot-separated config key (e.g., "sandbox.timeout").
	Path string

	// Value is the current value (as it should appear in YAML).
	Value any

	// Default is the base config value; if Value == Default, the field is omitted.
	Default any
}
