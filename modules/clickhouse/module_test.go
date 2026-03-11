package clickhouse

import (
	"errors"
	"testing"

	"github.com/ethpandaops/panda/pkg/types"
)

func TestValidateReturnsExampleLoaderError(t *testing.T) {
	t.Parallel()

	previousLoader := loadExampleCatalog
	loadExampleCatalog = func() (map[string]types.ExampleCategory, error) {
		return nil, errors.New("catalog failed")
	}
	defer func() {
		loadExampleCatalog = previousLoader
	}()

	module := New()
	if err := module.Validate(); err == nil || err.Error() != "catalog failed" {
		t.Fatalf("Validate() error = %v, want %q", err, "catalog failed")
	}
}
