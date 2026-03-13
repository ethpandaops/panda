package operations

type NoArgs struct{}

type Datasource struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Database    string `json:"database,omitempty"`
	URL         string `json:"url,omitempty"`
}

type DatasourcesPayload struct {
	Datasources []Datasource `json:"datasources"`
}

type ClickHouseQueryArgs struct {
	Datasource string         `json:"datasource,omitempty"`
	Cluster    string         `json:"cluster,omitempty"`
	SQL        string         `json:"sql"`
	Parameters map[string]any `json:"parameters,omitempty"`
}

type DatasourceArgs struct {
	Datasource string `json:"datasource"`
}

type DatasourceLabelArgs struct {
	Datasource string `json:"datasource"`
	Label      string `json:"label"`
}

type PrometheusQueryArgs struct {
	Datasource string `json:"datasource"`
	Query      string `json:"query"`
	Time       string `json:"time,omitempty"`
}

type PrometheusRangeQueryArgs struct {
	Datasource string `json:"datasource"`
	Query      string `json:"query"`
	Start      string `json:"start"`
	End        string `json:"end"`
	Step       string `json:"step"`
}

type LokiQueryArgs struct {
	Datasource string `json:"datasource"`
	Query      string `json:"query"`
	Limit      int    `json:"limit,omitempty"`
	Start      string `json:"start,omitempty"`
	End        string `json:"end,omitempty"`
	Direction  string `json:"direction,omitempty"`
}

type LokiInstantQueryArgs struct {
	Datasource string `json:"datasource"`
	Query      string `json:"query"`
	Limit      int    `json:"limit,omitempty"`
	Time       string `json:"time,omitempty"`
	Direction  string `json:"direction,omitempty"`
}

type LokiLabelsArgs struct {
	Datasource string `json:"datasource"`
	Start      string `json:"start,omitempty"`
	End        string `json:"end,omitempty"`
}

type LokiLabelValuesArgs struct {
	Datasource string `json:"datasource"`
	Label      string `json:"label"`
	Start      string `json:"start,omitempty"`
	End        string `json:"end,omitempty"`
}

type DoraNetwork struct {
	Name    string `json:"name"`
	DoraURL string `json:"dora_url"`
}

type DoraNetworksPayload struct {
	Networks []DoraNetwork `json:"networks"`
}

type DoraNetworkArgs struct {
	Network string `json:"network"`
}

type DoraBaseURLPayload struct {
	BaseURL string `json:"base_url"`
}

type DoraOverviewPayload struct {
	CurrentEpoch          any `json:"current_epoch"`
	CurrentSlot           any `json:"current_slot"`
	Finalized             any `json:"finalized"`
	ParticipationRate     any `json:"participation_rate"`
	ActiveValidatorCount  any `json:"active_validator_count,omitempty"`
	TotalValidatorCount   any `json:"total_validator_count,omitempty"`
	PendingValidatorCount any `json:"pending_validator_count,omitempty"`
	ExitedValidatorCount  any `json:"exited_validator_count,omitempty"`
}

type DoraIndexOrPubkeyArgs struct {
	Network       string `json:"network"`
	IndexOrPubkey string `json:"index_or_pubkey"`
}

type DoraSlotOrHashArgs struct {
	Network    string `json:"network"`
	SlotOrHash string `json:"slot_or_hash"`
}

type DoraEpochArgs struct {
	Network string `json:"network"`
	Epoch   string `json:"epoch"`
}

type DoraAddressArgs struct {
	Network string `json:"network"`
	Address string `json:"address"`
}

type DoraNumberOrHashArgs struct {
	Network      string `json:"network"`
	NumberOrHash string `json:"number_or_hash"`
}

type URLPayload struct {
	URL string `json:"url"`
}

type EthNodeNodeArgs struct {
	Network  string `json:"network"`
	Instance string `json:"instance"`
}

type EthNodeBeaconGetArgs struct {
	Network  string         `json:"network"`
	Instance string         `json:"instance"`
	Path     string         `json:"path"`
	Params   map[string]any `json:"params,omitempty"`
}

type EthNodeBeaconPostArgs struct {
	Network  string `json:"network"`
	Instance string `json:"instance"`
	Path     string `json:"path"`
	Body     any    `json:"body,omitempty"`
}

type EthNodeExecutionRPCArgs struct {
	Network  string `json:"network"`
	Instance string `json:"instance"`
	Method   string `json:"method"`
	Params   []any  `json:"params,omitempty"`
}

type EthNodeBeaconHeadersArgs struct {
	Network  string `json:"network"`
	Instance string `json:"instance"`
	Slot     string `json:"slot,omitempty"`
}

type EthNodeFinalityArgs struct {
	Network  string `json:"network"`
	Instance string `json:"instance"`
	StateID  string `json:"state_id,omitempty"`
}

type EthNodeBlockByNumberArgs struct {
	Network  string `json:"network"`
	Instance string `json:"instance"`
	Block    string `json:"block,omitempty"`
	FullTx   bool   `json:"full_tx,omitempty"`
}

type StatusCodePayload struct {
	StatusCode int `json:"status_code"`
}

type EthNodeVersionPayload struct {
	Data struct {
		Version string `json:"version"`
	} `json:"data"`
}

type EthNodeSyncingPayload struct {
	Data struct {
		HeadSlot     string `json:"head_slot"`
		SyncDistance string `json:"sync_distance"`
		IsSyncing    bool   `json:"is_syncing"`
		IsOptimistic bool   `json:"is_optimistic"`
		ELOffline    bool   `json:"el_offline"`
	} `json:"data"`
}

type EthNodePeerCountPayload struct {
	Data struct {
		Connected     string `json:"connected"`
		Disconnected  string `json:"disconnected"`
		Connecting    string `json:"connecting"`
		Disconnecting string `json:"disconnecting"`
	} `json:"data"`
}

type EthNodeCheckpoint struct {
	Epoch string `json:"epoch"`
	Root  string `json:"root,omitempty"`
}

type EthNodeFinalityPayload struct {
	Data struct {
		Finalized         EthNodeCheckpoint `json:"finalized"`
		CurrentJustified  EthNodeCheckpoint `json:"current_justified"`
		PreviousJustified EthNodeCheckpoint `json:"previous_justified"`
	} `json:"data"`
}

type EthNodeHeaderPayload struct {
	Data struct {
		Root   string `json:"root"`
		Header struct {
			Message struct {
				Slot          string `json:"slot"`
				ProposerIndex string `json:"proposer_index"`
				ParentRoot    string `json:"parent_root"`
				StateRoot     string `json:"state_root"`
				BodyRoot      string `json:"body_root"`
			} `json:"message"`
		} `json:"header"`
	} `json:"data"`
}

type EthNodeBlockNumberPayload struct {
	Hex         string `json:"hex"`
	BlockNumber uint64 `json:"block_number"`
}

type EthNodePeerCountRPCPayload struct {
	Hex       string `json:"hex"`
	PeerCount uint64 `json:"peer_count"`
}

type EthNodeChainIDPayload struct {
	Hex     string `json:"hex"`
	ChainID uint64 `json:"chain_id"`
}
