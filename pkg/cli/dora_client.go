package cli

import (
	"context"

	"github.com/ethpandaops/panda/pkg/operations"
)

func listDoraNetworks() ([]operations.DoraNetwork, error) {
	response, err := serverOperationJSON[operations.NoArgs, operations.DoraNetworksPayload](
		context.Background(),
		"dora.list_networks",
		operations.NoArgs{},
	)
	if err != nil {
		return nil, err
	}

	return response.Networks, nil
}

func doraOverview(args operations.DoraNetworkArgs) (operations.DoraOverviewPayload, error) {
	return serverOperationJSON[operations.DoraNetworkArgs, operations.DoraOverviewPayload](
		context.Background(),
		"dora.get_network_overview",
		args,
	)
}

func doraValidator(args operations.DoraIndexOrPubkeyArgs) (*rawServerResponse, error) {
	return serverOperationRaw(context.Background(), "dora.get_validator", args)
}

func doraSlot(args operations.DoraSlotOrHashArgs) (*rawServerResponse, error) {
	return serverOperationRaw(context.Background(), "dora.get_slot", args)
}

func doraEpoch(args operations.DoraEpochArgs) (*rawServerResponse, error) {
	return serverOperationRaw(context.Background(), "dora.get_epoch", args)
}
