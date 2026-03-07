package eips

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ethpandaops/mcp/pkg/types"
)

// ParseEIP extracts YAML frontmatter and body from EIP markdown content.
func ParseEIP(data []byte) (types.EIP, error) {
	frontmatter, body, err := splitFrontmatter(data)
	if err != nil {
		return types.EIP{}, err
	}

	var fm map[string]interface{}
	if err := yaml.Unmarshal(frontmatter, &fm); err != nil {
		return types.EIP{}, fmt.Errorf("unmarshaling frontmatter: %w", err)
	}

	number := toInt(fm["eip"])
	if number == 0 {
		return types.EIP{}, fmt.Errorf("missing or invalid eip number")
	}

	title := toString(fm["title"])
	if title == "" {
		title = fmt.Sprintf("EIP-%d", number)
	}

	return types.EIP{
		Number:      number,
		Title:       title,
		Description: toString(fm["description"]),
		Author:      toString(fm["author"]),
		Status:      toString(fm["status"]),
		Type:        toString(fm["type"]),
		Category:    toString(fm["category"]),
		Created:     toDateString(fm["created"]),
		Requires:    toString(fm["requires"]),
		Content:     strings.TrimSpace(string(body)),
		URL:         fmt.Sprintf("https://eips.ethereum.org/EIPS/eip-%d", number),
	}, nil
}

// splitFrontmatter separates YAML frontmatter from markdown body.
func splitFrontmatter(data []byte) (frontmatter, body []byte, err error) {
	const delimiter = "---"

	data = bytes.TrimSpace(data)
	if !bytes.HasPrefix(data, []byte(delimiter)) {
		return nil, nil, fmt.Errorf("file must start with YAML frontmatter delimiter '---'")
	}

	data = data[len(delimiter):]

	idx := bytes.Index(data, []byte("\n"+delimiter))
	if idx == -1 {
		return nil, nil, fmt.Errorf("missing closing frontmatter delimiter '---'")
	}

	frontmatter = bytes.TrimSpace(data[:idx])
	body = bytes.TrimSpace(data[idx+len("\n"+delimiter):])

	return frontmatter, body, nil
}

func toInt(v interface{}) int {
	switch t := v.(type) {
	case int:
		return t
	case float64:
		return int(t)
	case string:
		n, _ := strconv.Atoi(t)
		return n
	default:
		return 0
	}
}

func toString(v interface{}) string {
	if v == nil {
		return ""
	}

	switch t := v.(type) {
	case string:
		return t
	case int:
		return strconv.Itoa(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case []interface{}:
		parts := make([]string, len(t))
		for i, item := range t {
			parts[i] = fmt.Sprintf("%v", item)
		}

		return strings.Join(parts, ", ")
	default:
		return fmt.Sprintf("%v", v)
	}
}

func toDateString(v interface{}) string {
	if v == nil {
		return ""
	}

	switch t := v.(type) {
	case string:
		return t
	case time.Time:
		return t.Format("2006-01-02")
	default:
		return fmt.Sprintf("%v", v)
	}
}
