package proxy

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ethpandaops/panda/pkg/types"
)

func TestCloneDatasourceInfoClonesMetadataAndSkipsNamelessEntries(t *testing.T) {
	t.Parallel()

	assert.Nil(t, CloneDatasourceInfo(nil))

	infos := []types.DatasourceInfo{
		{
			Type: "clickhouse",
			Name: "xatu",
			Metadata: map[string]string{
				"cluster": "mainnet",
			},
		},
		{
			Type: "clickhouse",
			Name: "",
		},
	}

	cloned := CloneDatasourceInfo(infos)
	if assert.Len(t, cloned, 1) {
		assert.Equal(t, "xatu", cloned[0].Name)
		assert.Equal(t, "mainnet", cloned[0].Metadata["cluster"])
	}

	cloned[0].Metadata["cluster"] = "hoodi"
	assert.Equal(t, "mainnet", infos[0].Metadata["cluster"])
}
