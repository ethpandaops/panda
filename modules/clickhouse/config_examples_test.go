package clickhouse

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/types"
)

func TestSchemaDiscoveryConfigHonorsExplicitValues(t *testing.T) {
	var cfg SchemaDiscoveryConfig
	assert.True(t, cfg.IsEnabled())

	enabled := true
	cfg.Enabled = &enabled
	assert.True(t, cfg.IsEnabled())

	disabled := false
	cfg.Enabled = &disabled
	assert.False(t, cfg.IsEnabled())
}

func TestLoadExamplesReturnsFreshCatalog(t *testing.T) {
	first, err := loadExamples()
	require.NoError(t, err)
	second, err := loadExamples()
	require.NoError(t, err)
	require.NotEmpty(t, first)
	require.NotEmpty(t, second)

	var categoryName string
	for name := range first {
		categoryName = name
		break
	}

	category := first[categoryName]
	require.NotEmpty(t, category.Examples)
	category.Examples[0].Query = "SELECT 1"
	first[categoryName] = category

	assert.NotEqual(t, "SELECT 1", second[categoryName].Examples[0].Query)
}

func TestLoadExamplesPropagatesLoaderErrors(t *testing.T) {
	previousLoader := loadExampleCatalog
	loadExampleCatalog = func() (map[string]types.ExampleCategory, error) {
		return nil, errors.New("catalog failed")
	}
	t.Cleanup(func() { loadExampleCatalog = previousLoader })

	examples, err := loadExamples()
	require.Error(t, err)
	assert.Nil(t, examples)
	assert.EqualError(t, err, "catalog failed")
}
