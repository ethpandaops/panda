package loki

import (
	_ "embed"

	"github.com/ethpandaops/panda/pkg/module"
	"github.com/ethpandaops/panda/pkg/types"
)

//go:embed examples.yaml
var examplesYAML []byte

var loadExampleCatalog = module.NewExampleCatalogLoader(examplesYAML, "loki")

func loadExamples() (map[string]types.ExampleCategory, error) {
	return loadExampleCatalog()
}
