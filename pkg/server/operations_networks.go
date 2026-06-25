package server

import (
	"net/http"
	"sort"
	"strings"

	"github.com/ethpandaops/cartographoor/pkg/discovery"

	"github.com/ethpandaops/panda/pkg/operations"
)

// networkListItem is the compact shape for the network/devnet listing.
type networkListItem struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ChainID     uint64 `json:"chain_id,omitempty"`
	Status      string `json:"status"`
	IsDevnet    bool   `json:"is_devnet"`
	DevnetGroup string `json:"devnet_group,omitempty"`
}

// networkListResponse is the data payload for network.list.
type networkListResponse struct {
	Networks           []networkListItem   `json:"networks"`
	Groups             []string            `json:"groups"`
	ActiveDevnetGroups map[string][]string `json:"active_devnet_groups"`
}

// networkForkConsensus describes a consensus-layer fork activation.
type networkForkConsensus struct {
	Epoch             uint64            `json:"epoch"`
	Timestamp         uint64            `json:"timestamp,omitempty"`
	MinClientVersions map[string]string `json:"min_client_versions,omitempty"`
}

// networkForkExecution describes an execution-layer fork activation.
type networkForkExecution struct {
	Block     uint64 `json:"block"`
	Timestamp uint64 `json:"timestamp,omitempty"`
}

// networkForks groups consensus and execution fork schedules.
type networkForks struct {
	Consensus map[string]networkForkConsensus `json:"consensus,omitempty"`
	Execution map[string]networkForkExecution `json:"execution,omitempty"`
}

// networkImage is a client or tool image name/version pair.
type networkImage struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// networkDetail is the curated devnet descriptor returned by network.get. It
// normalizes the upstream Cartographoor model into snake_case, devnet-focused
// fields (forks, client images, service endpoints, genesis).
type networkDetail struct {
	ID               string                   `json:"id"`
	Name             string                   `json:"name"`
	ChainID          uint64                   `json:"chain_id,omitempty"`
	Status           string                   `json:"status"`
	IsDevnet         bool                     `json:"is_devnet"`
	DevnetGroup      string                   `json:"devnet_group,omitempty"`
	Description      string                   `json:"description,omitempty"`
	Repository       string                   `json:"repository,omitempty"`
	GenesisTime      uint64                   `json:"genesis_time,omitempty"`
	GenesisDelay     uint64                   `json:"genesis_delay,omitempty"`
	Forks            *networkForks            `json:"forks,omitempty"`
	Clients          []networkImage           `json:"clients,omitempty"`
	Tools            []networkImage           `json:"tools,omitempty"`
	Endpoints        map[string]string        `json:"endpoints,omitempty"`
	BlobSchedule     []discovery.BlobSchedule `json:"blob_schedule,omitempty"`
	Links            []discovery.Link         `json:"links,omitempty"`
	NodeInventoryURL string                   `json:"node_inventory_url,omitempty"`
	HiveURL          string                   `json:"hive_url,omitempty"`
}

// networkGroupResponse is the data payload for network.group.
type networkGroupResponse struct {
	Group    string            `json:"group"`
	Networks []networkListItem `json:"networks"`
}

func (s *service) handleNetworkOperation(operationID string, w http.ResponseWriter, r *http.Request) bool {
	switch operationID {
	case "network.list":
		s.handleNetworkList(w, r)
	case "network.get":
		s.handleNetworkGet(w, r)
	case "network.group":
		s.handleNetworkGroup(w, r)
	case "network.spec":
		s.handleNetworkSpec(w, r)
	default:
		return false
	}

	return true
}

func (s *service) handleNetworkList(w http.ResponseWriter, r *http.Request) {
	if s.cartographoorClient == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "network inventory not available")
		return
	}

	req, err := decodeOperationRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	activeOnly := true
	if value, boolErr := optionalBoolArg(req.Args, "active"); boolErr != nil {
		writeAPIError(w, http.StatusBadRequest, boolErr.Error())
		return
	} else if value != nil {
		activeOnly = *value
	}

	devnetsOnly := false
	if value, boolErr := optionalBoolArg(req.Args, "devnets_only"); boolErr != nil {
		writeAPIError(w, http.StatusBadRequest, boolErr.Error())
		return
	} else if value != nil {
		devnetsOnly = *value
	}

	networks := s.cartographoorClient.GetAllNetworks()
	if activeOnly {
		networks = s.cartographoorClient.GetActiveNetworks()
	}

	groupIndex := s.networkGroupIndex()

	items := make([]networkListItem, 0, len(networks))

	for id, network := range networks {
		group := groupIndex[id]
		if devnetsOnly && group == "" {
			continue
		}

		items = append(items, networkListItem{
			ID:          id,
			Name:        network.Name,
			ChainID:     network.ChainID,
			Status:      network.Status,
			IsDevnet:    group != "",
			DevnetGroup: group,
		})
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].ID < items[j].ID
	})

	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: networkListResponse{
			Networks:           items,
			Groups:             sortedDevnetGroups(s.cartographoorClient.GetActiveGroups()),
			ActiveDevnetGroups: s.cartographoorClient.GetActiveGroups(),
		},
	})
}

func (s *service) handleNetworkGet(w http.ResponseWriter, r *http.Request) {
	if s.cartographoorClient == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "network inventory not available")
		return
	}

	req, err := decodeOperationRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	id, err := requiredStringArg(req.Args, "network")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	network, ok := s.cartographoorClient.GetNetwork(id)
	if !ok {
		writeAPIError(w, http.StatusNotFound, "network not found: "+id+". Use network.list to list current ids.")
		return
	}

	group := s.networkGroupIndex()[id]

	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: buildNetworkDetail(id, group, network),
	})
}

func (s *service) handleNetworkGroup(w http.ResponseWriter, r *http.Request) {
	if s.cartographoorClient == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "network inventory not available")
		return
	}

	req, err := decodeOperationRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	name, err := requiredStringArg(req.Args, "group")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	networks, ok := s.cartographoorClient.GetGroup(name)
	if !ok {
		groups := strings.Join(s.cartographoorClient.GetGroups(), ", ")
		writeAPIError(w, http.StatusNotFound, "devnet group not found: "+name+". Available groups: "+groups)
		return
	}

	items := make([]networkListItem, 0, len(networks))

	for id, network := range networks {
		items = append(items, networkListItem{
			ID:          id,
			Name:        network.Name,
			ChainID:     network.ChainID,
			Status:      network.Status,
			IsDevnet:    true,
			DevnetGroup: name,
		})
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].ID < items[j].ID
	})

	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: networkGroupResponse{Group: name, Networks: items},
	})
}

// networkGroupIndex builds an id -> devnet-group lookup across all known groups.
func (s *service) networkGroupIndex() map[string]string {
	index := make(map[string]string)

	for _, group := range s.cartographoorClient.GetGroups() {
		networks, ok := s.cartographoorClient.GetGroup(group)
		if !ok {
			continue
		}

		for id := range networks {
			index[id] = group
		}
	}

	return index
}

// buildNetworkDetail normalizes a Cartographoor network into the curated,
// devnet-focused detail shape.
func buildNetworkDetail(id, group string, network discovery.Network) networkDetail {
	detail := networkDetail{
		ID:               id,
		Name:             network.Name,
		ChainID:          network.ChainID,
		Status:           network.Status,
		IsDevnet:         group != "",
		DevnetGroup:      group,
		Description:      network.Description,
		Repository:       network.Repository,
		Forks:            buildNetworkForks(network.Forks),
		Clients:          networkImages(clientImages(network.Images)),
		Tools:            networkImages(toolImages(network.Images)),
		Endpoints:        networkEndpoints(network.ServiceURLs),
		BlobSchedule:     network.BlobSchedule,
		Links:            network.Links,
		NodeInventoryURL: networkNodeInventoryURL(network),
		HiveURL:          network.HiveURL,
	}

	if network.GenesisConfig != nil {
		detail.GenesisTime = network.GenesisConfig.GenesisTime
		detail.GenesisDelay = network.GenesisConfig.GenesisDelay
	}

	return detail
}

func buildNetworkForks(forks *discovery.ForksConfig) *networkForks {
	if forks == nil || (len(forks.Consensus) == 0 && len(forks.Execution) == 0) {
		return nil
	}

	result := &networkForks{}

	if len(forks.Consensus) > 0 {
		result.Consensus = make(map[string]networkForkConsensus, len(forks.Consensus))
		for name, fork := range forks.Consensus {
			result.Consensus[name] = networkForkConsensus{
				Epoch:             fork.Epoch,
				Timestamp:         fork.Timestamp,
				MinClientVersions: fork.MinClientVersions,
			}
		}
	}

	if len(forks.Execution) > 0 {
		result.Execution = make(map[string]networkForkExecution, len(forks.Execution))
		for name, fork := range forks.Execution {
			result.Execution[name] = networkForkExecution{
				Block:     fork.Block,
				Timestamp: fork.Timestamp,
			}
		}
	}

	return result
}

func clientImages(images *discovery.Images) []discovery.ClientImage {
	if images == nil {
		return nil
	}

	return images.Clients
}

func toolImages(images *discovery.Images) []discovery.ToolImage {
	if images == nil {
		return nil
	}

	return images.Tools
}

func networkImages[T discovery.ClientImage | discovery.ToolImage](images []T) []networkImage {
	if len(images) == 0 {
		return nil
	}

	result := make([]networkImage, 0, len(images))

	for _, image := range images {
		switch v := any(image).(type) {
		case discovery.ClientImage:
			result = append(result, networkImage{Name: v.Name, Version: v.Version})
		case discovery.ToolImage:
			result = append(result, networkImage{Name: v.Name, Version: v.Version})
		}
	}

	return result
}

// networkEndpoints flattens the non-empty service URLs into a friendly,
// snake_case map keyed by role.
func networkEndpoints(urls *discovery.ServiceURLs) map[string]string {
	if urls == nil {
		return nil
	}

	candidates := map[string]string{
		"rpc":             urls.JSONRPC,
		"beacon":          urls.BeaconRPC,
		"explorer":        urls.Explorer,
		"beacon_explorer": urls.BeaconExplorer,
		"dora":            urls.Dora,
		"forky":           urls.Forky,
		"tracoor":         urls.Tracoor,
		"cbt":             urls.Cbt,
		"cbt_api":         urls.CbtApi,
		"faucet":          urls.Faucet,
		"checkpoint_sync": urls.CheckpointSync,
		"blobscan":        urls.Blobscan,
		"blob_archive":    urls.BlobArchive,
		"assertoor":       urls.Assertoor,
		"spamoor":         urls.Spamoor,
		"syncoor":         urls.Syncoor,
		"buildoor":        urls.Buildoor,
		"ethstats":        urls.Ethstats,
		"forkmon":         urls.Forkmon,
		"devnet_spec":     urls.DevnetSpec,
	}

	result := make(map[string]string, len(candidates))

	for role, url := range candidates {
		if url != "" {
			result[role] = url
		}
	}

	if len(result) == 0 {
		return nil
	}

	return result
}

// networkNodeInventoryURL extracts the node inventory API URL, when advertised.
func networkNodeInventoryURL(network discovery.Network) string {
	if network.GenesisConfig == nil {
		return ""
	}

	for _, cfg := range network.GenesisConfig.API {
		if strings.Contains(cfg.Path, "/nodes/inventory") || strings.Contains(cfg.URL, "/nodes/inventory") {
			return cfg.URL
		}
	}

	return ""
}

func sortedDevnetGroups(groups map[string][]string) []string {
	names := make([]string, 0, len(groups))

	for name, networks := range groups {
		if len(networks) > 0 {
			names = append(names, name)
		}
	}

	sort.Strings(names)

	return names
}
