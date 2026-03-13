package loki

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadExamplesReturnsFreshCatalog(t *testing.T) {
	first, err := loadExamples()
	require.NoError(t, err)
	second, err := loadExamples()
	require.NoError(t, err)
	require.NotEmpty(t, first)

	var categoryName string
	for name := range first {
		categoryName = name
		break
	}

	category := first[categoryName]
	require.NotEmpty(t, category.Examples)
	category.Examples[0].Query = "mutated"
	first[categoryName] = category

	assert.NotEqual(t, "mutated", second[categoryName].Examples[0].Query)
}
