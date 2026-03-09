"""Thin ethnode wrappers over proxy operations."""

from __future__ import annotations

import os
from typing import Any

from ethpandaops import _runtime


def _require_ethnode_available() -> None:
    if not os.environ.get("ETHPANDAOPS_ETHNODE_AVAILABLE", "").strip():
        raise ValueError("Ethnode is not enabled or no node access is available.")


def beacon_get(
    network: str,
    instance: str,
    path: str,
    params: dict[str, Any] | None = None,
) -> dict[str, Any]:
    _require_ethnode_available()
    payload = _runtime.invoke_json(
        "ethnode.beacon_get",
        {
            "network": network,
            "instance": instance,
            "path": path,
            "params": params,
        },
    )
    return payload if isinstance(payload, dict) else {}


def beacon_post(
    network: str,
    instance: str,
    path: str,
    body: Any | None = None,
) -> dict[str, Any]:
    _require_ethnode_available()
    payload = _runtime.invoke_json(
        "ethnode.beacon_post",
        {
            "network": network,
            "instance": instance,
            "path": path,
            "body": body,
        },
    )
    return payload if isinstance(payload, dict) else {}


def execution_rpc(
    network: str,
    instance: str,
    method: str,
    params: list[Any] | None = None,
) -> Any:
    _require_ethnode_available()
    data = _runtime.invoke_json(
        "ethnode.execution_rpc",
        {
            "network": network,
            "instance": instance,
            "method": method,
            "params": params,
        },
    )
    if isinstance(data, dict):
        return data.get("result")
    return None


def get_node_version(network: str, instance: str) -> dict[str, Any]:
    _require_ethnode_available()
    return _runtime.invoke_data(
        "ethnode.get_node_version",
        {"network": network, "instance": instance},
    )


def get_node_syncing(network: str, instance: str) -> dict[str, Any]:
    _require_ethnode_available()
    return _runtime.invoke_data(
        "ethnode.get_node_syncing",
        {"network": network, "instance": instance},
    )


def get_node_health(network: str, instance: str) -> int:
    _require_ethnode_available()
    data = _runtime.invoke_data(
        "ethnode.get_node_health",
        {"network": network, "instance": instance},
    )
    return data.get("status_code", 0)


def get_peers(network: str, instance: str) -> dict[str, Any]:
    _require_ethnode_available()
    return _runtime.invoke_data(
        "ethnode.get_peers",
        {"network": network, "instance": instance},
    )


def get_peer_count(network: str, instance: str) -> dict[str, Any]:
    _require_ethnode_available()
    return _runtime.invoke_data(
        "ethnode.get_peer_count",
        {"network": network, "instance": instance},
    )


def get_beacon_headers(
    network: str, instance: str, slot: str = "head"
) -> dict[str, Any]:
    _require_ethnode_available()
    return _runtime.invoke_data(
        "ethnode.get_beacon_headers",
        {"network": network, "instance": instance, "slot": slot},
    )


def get_finality_checkpoints(
    network: str, instance: str, state_id: str = "head"
) -> dict[str, Any]:
    _require_ethnode_available()
    return _runtime.invoke_data(
        "ethnode.get_finality_checkpoints",
        {"network": network, "instance": instance, "state_id": state_id},
    )


def get_config_spec(network: str, instance: str) -> dict[str, Any]:
    _require_ethnode_available()
    return _runtime.invoke_data(
        "ethnode.get_config_spec",
        {"network": network, "instance": instance},
    )


def get_fork_schedule(network: str, instance: str) -> dict[str, Any]:
    _require_ethnode_available()
    return _runtime.invoke_data(
        "ethnode.get_fork_schedule",
        {"network": network, "instance": instance},
    )


def get_deposit_contract(network: str, instance: str) -> dict[str, Any]:
    _require_ethnode_available()
    return _runtime.invoke_data(
        "ethnode.get_deposit_contract",
        {"network": network, "instance": instance},
    )


def eth_block_number(network: str, instance: str) -> int:
    _require_ethnode_available()
    data = _runtime.invoke_data(
        "ethnode.eth_block_number",
        {"network": network, "instance": instance},
    )
    return data.get("block_number", 0)


def eth_syncing(network: str, instance: str) -> dict[str, Any] | bool:
    _require_ethnode_available()
    return _runtime.invoke_data(
        "ethnode.eth_syncing",
        {"network": network, "instance": instance},
    )


def eth_chain_id(network: str, instance: str) -> int:
    _require_ethnode_available()
    data = _runtime.invoke_data(
        "ethnode.eth_chain_id",
        {"network": network, "instance": instance},
    )
    return data.get("chain_id", 0)


def eth_get_block_by_number(
    network: str,
    instance: str,
    block: str = "latest",
    full_tx: bool = False,
) -> dict[str, Any]:
    _require_ethnode_available()
    return _runtime.invoke_data(
        "ethnode.eth_get_block_by_number",
        {
            "network": network,
            "instance": instance,
            "block": block,
            "full_tx": full_tx,
        },
    )


def net_peer_count(network: str, instance: str) -> int:
    _require_ethnode_available()
    data = _runtime.invoke_data(
        "ethnode.net_peer_count",
        {"network": network, "instance": instance},
    )
    return data.get("peer_count", 0)


def web3_client_version(network: str, instance: str) -> str:
    _require_ethnode_available()
    data = _runtime.invoke_data(
        "ethnode.web3_client_version",
        {"network": network, "instance": instance},
    )
    if isinstance(data, str):
        return data
    return ""
