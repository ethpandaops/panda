package cli

import (
	"context"

	"github.com/ethpandaops/panda/pkg/operations"
)

func ethNodeSyncing(args operations.EthNodeNodeArgs) (operations.EthNodeSyncingPayload, error) {
	return serverOperationJSON[operations.EthNodeNodeArgs, operations.EthNodeSyncingPayload](
		context.Background(),
		"ethnode.get_node_syncing",
		args,
	)
}

func ethNodeVersion(args operations.EthNodeNodeArgs) (operations.EthNodeVersionPayload, error) {
	return serverOperationJSON[operations.EthNodeNodeArgs, operations.EthNodeVersionPayload](
		context.Background(),
		"ethnode.get_node_version",
		args,
	)
}

func ethNodeExecutionClientVersion(args operations.EthNodeNodeArgs) (string, error) {
	return serverOperationJSON[operations.EthNodeNodeArgs, string](
		context.Background(),
		"ethnode.web3_client_version",
		args,
	)
}

func ethNodeHealth(args operations.EthNodeNodeArgs) (operations.StatusCodePayload, error) {
	return serverOperationJSON[operations.EthNodeNodeArgs, operations.StatusCodePayload](
		context.Background(),
		"ethnode.get_node_health",
		args,
	)
}

func ethNodePeerCount(args operations.EthNodeNodeArgs) (operations.EthNodePeerCountPayload, error) {
	return serverOperationJSON[operations.EthNodeNodeArgs, operations.EthNodePeerCountPayload](
		context.Background(),
		"ethnode.get_peer_count",
		args,
	)
}

func ethNodeFinality(args operations.EthNodeFinalityArgs) (operations.EthNodeFinalityPayload, error) {
	return serverOperationJSON[operations.EthNodeFinalityArgs, operations.EthNodeFinalityPayload](
		context.Background(),
		"ethnode.get_finality_checkpoints",
		args,
	)
}

func ethNodeHeaders(args operations.EthNodeBeaconHeadersArgs) (operations.EthNodeHeaderPayload, error) {
	return serverOperationJSON[operations.EthNodeBeaconHeadersArgs, operations.EthNodeHeaderPayload](
		context.Background(),
		"ethnode.get_beacon_headers",
		args,
	)
}

func ethNodeBlockNumber(args operations.EthNodeNodeArgs) (operations.EthNodeBlockNumberPayload, error) {
	return serverOperationJSON[operations.EthNodeNodeArgs, operations.EthNodeBlockNumberPayload](
		context.Background(),
		"ethnode.eth_block_number",
		args,
	)
}

func ethNodeBeaconGet(args operations.EthNodeBeaconGetArgs) (*rawServerResponse, error) {
	return serverOperationRaw(context.Background(), "ethnode.beacon_get", args)
}

func ethNodeExecutionRPC(args operations.EthNodeExecutionRPCArgs) (*rawServerResponse, error) {
	return serverOperationRaw(context.Background(), "ethnode.execution_rpc", args)
}
