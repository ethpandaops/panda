package clickhouse

import (
	_ "embed"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ethpandaops/mcp/pkg/types"
)

//go:embed examples.yaml
var examplesYAML []byte

var queryExamples map[string]types.ExampleCategory

func init() {
	if err := yaml.Unmarshal(examplesYAML, &queryExamples); err != nil {
		panic(fmt.Sprintf("failed to parse clickhouse examples.yaml: %v", err))
	}
	// Trim trailing whitespace from queries
	for key, category := range queryExamples {
		for i := range category.Examples {
			category.Examples[i].Query = strings.TrimSpace(category.Examples[i].Query)
		}
		queryExamples[key] = category
	}
}
