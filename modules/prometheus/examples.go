package prometheus

import (
	_ "embed"

	"github.com/ethpandaops/panda/pkg/module"
	"github.com/ethpandaops/panda/pkg/types"
)

//go:embed examples.yaml
var examplesYAML []byte

func loadExamples() (map[string]types.ExampleCategory, error) {
	return module.LoadExampleCatalog(examplesYAML, "prometheus")
}
