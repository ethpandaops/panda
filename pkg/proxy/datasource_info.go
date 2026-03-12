package proxy

import (
	"maps"

	"github.com/ethpandaops/panda/pkg/serverapi"
	"github.com/ethpandaops/panda/pkg/types"
)

func CloneDatasourceInfo(infos []types.DatasourceInfo) []types.DatasourceInfo {
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

func CloneDatasourcesResponse(response serverapi.DatasourcesResponse) serverapi.DatasourcesResponse {
	response.Datasources = CloneDatasourceInfo(response.Datasources)

	return response
}

func FilterDatasourceInfoByType(infos []types.DatasourceInfo, kind string) []types.DatasourceInfo {
	if len(infos) == 0 {
		return nil
	}

	filtered := make([]types.DatasourceInfo, 0, len(infos))
	for _, info := range infos {
		if info.Type != kind || info.Name == "" {
			continue
		}

		filtered = append(filtered, info)
	}

	return filtered
}

func DatasourceNames(infos []types.DatasourceInfo) []string {
	if len(infos) == 0 {
		return nil
	}

	names := make([]string, 0, len(infos))
	for _, info := range infos {
		if info.Name != "" {
			names = append(names, info.Name)
		}
	}

	return names
}
