package resource

import (
	"github.com/ethpandaops/panda/pkg/module"
	"github.com/ethpandaops/panda/pkg/types"
)

// GetQueryExamples returns query examples from initialized modules only.
// Examples are surfaced through the semantic search tool, not as a bulk
// resource: the full set is far too large to hand to a model wholesale.
func GetQueryExamples(moduleReg *module.Registry) map[string]types.ExampleCategory {
	return moduleReg.Examples()
}
