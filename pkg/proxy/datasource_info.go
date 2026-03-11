package proxy

import (
	"maps"

	"github.com/ethpandaops/panda/pkg/types"
)

func cloneDatasourceInfo(infos []types.DatasourceInfo) []types.DatasourceInfo {
	if len(infos) == 0 {
		return nil
	}

	cloned := make([]types.DatasourceInfo, 0, len(infos))
	for _, info := range infos {
		if info.Name == "" {
			continue
		}

		copyInfo := info
		if info.Metadata != nil {
			copyInfo.Metadata = maps.Clone(info.Metadata)
		}

		cloned = append(cloned, copyInfo)
	}

	return cloned
}
