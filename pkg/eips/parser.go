package eips

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ethpandaops/panda/pkg/types"
)

// ParseEIP parses an EIP markdown file with YAML frontmatter.
func ParseEIP(data []byte) (types.EIP, error) {
	frontmatter, body, err := splitFrontmatter(data)
	if err != nil {
		return types.EIP{}, err
	}

	var meta map[string]any
	if err := yaml.Unmarshal(frontmatter, &meta); err != nil {
		return types.EIP{}, fmt.Errorf("parsing frontmatter YAML: %w", err)
	}

	number := toInt(meta["eip"])
	if number == 0 {
		return types.EIP{}, fmt.Errorf("missing or zero eip number")
	}

	title := toString(meta["title"])
	if title == "" {
		title = fmt.Sprintf("EIP-%d", number)
	}

	return types.EIP{
		Number:      number,
		Title:       title,
		Description: toString(meta["description"]),
		Author:      toString(meta["author"]),
		Status:      toString(meta["status"]),
		Type:        toString(meta["type"]),
		Category:    toString(meta["category"]),
		Created:     toDateString(meta["created"]),
		Requires:    toString(meta["requires"]),
		Content:     strings.TrimSpace(string(body)),
		URL:         fmt.Sprintf("https://eips.ethereum.org/EIPS/eip-%d", number),
	}, nil
}

func splitFrontmatter(data []byte) ([]byte, []byte, error) {
	const delimiter = "---"

	content := bytes.TrimSpace(data)
	if !bytes.HasPrefix(content, []byte(delimiter)) {
		return nil, nil, fmt.Errorf("no frontmatter delimiter found")
	}

	content = content[len(delimiter):]

	idx := bytes.Index(content, []byte("\n"+delimiter))
	if idx < 0 {
		return nil, nil, fmt.Errorf("no closing frontmatter delimiter found")
	}

	frontmatter := content[:idx]
	body := content[idx+len(delimiter)+1:]

	return frontmatter, body, nil
}

func toInt(v any) int {
	switch val := v.(type) {
	case int:
		return val
	case float64:
		return int(val)
	default:
		return 0
	}
}

func toString(v any) string {
	switch val := v.(type) {
	case string:
		return strings.TrimSpace(val)
	case int:
		return fmt.Sprintf("%d", val)
	case float64:
		return fmt.Sprintf("%.0f", val)
	case []any:
		parts := make([]string, 0, len(val))
		for _, item := range val {
			if s := toString(item); s != "" {
				parts = append(parts, s)
			}
		}

		return strings.Join(parts, ", ")
	default:
		return ""
	}
}

func toDateString(v any) string {
	switch val := v.(type) {
	case string:
		return strings.TrimSpace(val)
	case time.Time:
		return val.Format("2006-01-02")
	default:
		return ""
	}
}
