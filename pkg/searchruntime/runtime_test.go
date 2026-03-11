package searchruntime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/embedding"
	"github.com/ethpandaops/panda/pkg/resource"
	"github.com/ethpandaops/panda/runbooks"
)

func TestRuntimeCloseReleasesAllResources(t *testing.T) {
	runtime := &Runtime{
		ExampleIndex:    &resource.ExampleIndex{},
		RunbookRegistry: &runbooks.Registry{},
		RunbookIndex:    &resource.RunbookIndex{},
		embedder:        &embedding.Embedder{},
	}

	require.NoError(t, runtime.Close())
	assert.Nil(t, runtime.ExampleIndex)
	assert.Nil(t, runtime.RunbookRegistry)
	assert.Nil(t, runtime.RunbookIndex)
	assert.Nil(t, runtime.embedder)
}
