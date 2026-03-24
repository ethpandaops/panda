package consensusspecs

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ethpandaops/panda/pkg/types"
)

// ParseSpec parses a consensus-specs markdown document into a ConsensusSpec.
func ParseSpec(
	fork, topic string,
	data []byte,
	repository, ref string,
) types.ConsensusSpec {
	title := extractTitle(data)
	if title == "" {
		title = fmt.Sprintf("%s/%s", fork, topic)
	}

	return types.ConsensusSpec{
		Fork:    fork,
		Topic:   topic,
		Title:   title,
		Content: strings.TrimSpace(string(data)),
		URL:     specGitHubURL(repository, ref, fork, topic),
	}
}

// ParsePreset parses a YAML preset file into a slice of SpecConstants.
func ParsePreset(fork string, data []byte) ([]types.SpecConstant, error) {
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing preset YAML: %w", err)
	}

	constants := make([]types.SpecConstant, 0, len(raw))

	for name, val := range raw {
		// Skip YAML comments-only keys or non-constant entries.
		if strings.HasPrefix(name, "#") || name == "" {
			continue
		}

		constants = append(constants, types.SpecConstant{
			Name:  name,
			Value: formatValue(val),
			Fork:  fork,
		})
	}

	return constants, nil
}

// extractTitle finds the first markdown heading (# Title) in the data.
func extractTitle(data []byte) string {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if title, ok := strings.CutPrefix(line, "# "); ok {
			return strings.TrimSpace(title)
		}
	}

	return ""
}

func formatValue(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case int:
		return fmt.Sprintf("%d", val)
	case int64:
		return fmt.Sprintf("%d", val)
	case float64:
		if val == float64(int64(val)) {
			return fmt.Sprintf("%d", int64(val))
		}

		return fmt.Sprintf("%g", val)
	case bool:
		return fmt.Sprintf("%t", val)
	default:
		return fmt.Sprintf("%v", val)
	}
}
