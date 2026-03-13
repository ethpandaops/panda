package dora

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/types"
)

func TestLoadExamplesReturnsIndependentCatalogs(t *testing.T) {
	first, err := loadExamples()
	require.NoError(t, err)
	second, err := loadExamples()
	require.NoError(t, err)
	require.NotEmpty(t, first)
	require.NotEmpty(t, second)

	for name, category := range first {
		require.NotEmpty(t, category.Examples)
		category.Examples[0].Name = "mutated"
		first[name] = category
		assert.NotEqual(t, "mutated", second[name].Examples[0].Name)
		return
	}

	t.Fatal("expected at least one example category")
}

func TestLoadExamplesReturnsLoaderErrors(t *testing.T) {
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
