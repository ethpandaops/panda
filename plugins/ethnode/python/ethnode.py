"""Direct access to Ethereum beacon and execution node APIs via credential proxy.

This module provides curated functions for common beacon/execution node API calls
plus generic pass-through for any endpoint. All requests go through the credential
proxy - credentials are never exposed to sandbox containers.

Node instances follow the naming convention: {client_cl}-{client_el}-{index}
(e.g., "lighthouse-geth-1", "prysm-nethermind-2").

Example:
    from ethpandaops import ethnode

    # Check beacon node sync status
    syncing = ethnode.get_node_syncing("my-devnet", "lighthouse-geth-1")
    print(f"Head slot: {syncing['data']['head_slot']}")

    # Check EL block number
    block_num = ethnode.eth_block_number("my-devnet", "lighthouse-geth-1")
    print(f"Latest block: {block_num}")
"""

import os
from typing import Any

import httpx

# Proxy configuration (required).
_PROXY_URL = os.environ.get("ETHPANDAOPS_PROXY_URL", "")
_PROXY_TOKEN = os.environ.get("ETHPANDAOPS_PROXY_TOKEN", "")

# JSON-RPC request ID counter.
_rpc_id = 0


def _check_proxy_config() -> None:
    """Verify proxy is configured."""
    if not _PROXY_URL or not _PROXY_TOKEN:
        raise ValueError(
            "Proxy not configured. ETHPANDAOPS_PROXY_URL and ETHPANDAOPS_PROXY_TOKEN are required."
        )


def _get_client() -> httpx.Client:
    """Get an HTTP client configured for the proxy."""
    _check_proxy_config()

    return httpx.Client(
        base_url=_PROXY_URL,
        headers={
            "Authorization": f"Bearer {_PROXY_TOKEN}",
        },
        timeout=httpx.Timeout(connect=5.0, read=30.0, write=10.0, pool=5.0),
    )


# ---------------------------------------------------------------------------
# Generic pass-through functions
# ---------------------------------------------------------------------------


def beacon_get(
    network: str,
    instance: str,
    path: str,
    params: dict[str, Any] | None = None,
) -> dict[str, Any]:
    """GET any beacon API endpoint.

    Args:
        network: Network name (e.g., "dencun-devnet-12").
        instance: Node instance name (e.g., "lighthouse-geth-1").
        path: Beacon API path (e.g., "/eth/v1/node/version").
        params: Optional query parameters.

    Returns:
        Parsed JSON response.
    """
    with _get_client() as client:
        url = f"/beacon/{network}/{instance}{path}"
        response = client.get(url, params=params)

        if not response.is_success:
            raise ValueError(
                f"Beacon API request failed (HTTP {response.status_code}): {response.text}"
            )

        return response.json()


def beacon_post(
    network: str,
    instance: str,
    path: str,
    body: Any | None = None,
) -> dict[str, Any]:
    """POST any beacon API endpoint.

    Args:
        network: Network name.
        instance: Node instance name.
        path: Beacon API path.
        body: Optional JSON request body.

    Returns:
        Parsed JSON response.
    """
    with _get_client() as client:
        url = f"/beacon/{network}/{instance}{path}"
        response = client.post(url, json=body)

        if not response.is_success:
            raise ValueError(
                f"Beacon API request failed (HTTP {response.status_code}): {response.text}"
            )

        return response.json()


def execution_rpc(
    network: str,
    instance: str,
    method: str,
    params: list[Any] | None = None,
) -> Any:
    """Call any JSON-RPC method on an execution node.

    Args:
        network: Network name.
        instance: Node instance name.
        method: JSON-RPC method name (e.g., "eth_blockNumber").
        params: Optional JSON-RPC parameters list.

    Returns:
        The 'result' field from the JSON-RPC response.
    """
    global _rpc_id
    _rpc_id += 1

    payload = {
        "jsonrpc": "2.0",
        "method": method,
        "params": params or [],
        "id": _rpc_id,
    }

    with _get_client() as client:
        url = f"/execution/{network}/{instance}/"
        response = client.post(url, json=payload)

        if not response.is_success:
            raise ValueError(
                f"Execution RPC request failed (HTTP {response.status_code}): {response.text}"
            )

        data = response.json()

        if "error" in data:
            err = data["error"]
            raise ValueError(
                f"JSON-RPC error {err.get('code', '?')}: {err.get('message', 'unknown')}"
            )

        return data.get("result")


# ---------------------------------------------------------------------------
# Curated Beacon Node (CL) functions
# ---------------------------------------------------------------------------


def get_node_version(network: str, instance: str) -> dict[str, Any]:
    """Get beacon node software version.

    Args:
        network: Network name.
        instance: Node instance name.

    Returns:
        Response with data.version string.
    """
    return beacon_get(network, instance, "/eth/v1/node/version")


def get_node_syncing(network: str, instance: str) -> dict[str, Any]:
    """Get beacon node sync status.

    Args:
        network: Network name.
        instance: Node instance name.

    Returns:
        Response with data containing head_slot, sync_distance, is_syncing.
    """
    return beacon_get(network, instance, "/eth/v1/node/syncing")


def get_node_health(network: str, instance: str) -> int:
    """Get beacon node health status code.

    Args:
        network: Network name.
        instance: Node instance name.

    Returns:
        HTTP status code (200=healthy, 206=syncing, 503=not initialized).
    """
    with _get_client() as client:
        url = f"/beacon/{network}/{instance}/eth/v1/node/health"
        response = client.get(url)

        return response.status_code


def get_peers(network: str, instance: str) -> dict[str, Any]:
    """Get connected peers list.

    Args:
        network: Network name.
        instance: Node instance name.

    Returns:
        Response with data containing list of peer objects.
    """
    return beacon_get(network, instance, "/eth/v1/node/peers")


def get_peer_count(network: str, instance: str) -> dict[str, Any]:
    """Get peer count summary.

    Args:
        network: Network name.
        instance: Node instance name.

    Returns:
        Response with data containing connected, disconnected, connecting, disconnecting counts.
    """
    return beacon_get(network, instance, "/eth/v1/node/peer_count")


def get_beacon_headers(
    network: str, instance: str, slot: str = "head"
) -> dict[str, Any]:
    """Get beacon block header.

    Args:
        network: Network name.
        instance: Node instance name.
        slot: Slot number or "head" (default: "head").

    Returns:
        Response with header data.
    """
    return beacon_get(network, instance, f"/eth/v1/beacon/headers/{slot}")


def get_finality_checkpoints(
    network: str, instance: str, state_id: str = "head"
) -> dict[str, Any]:
    """Get finality checkpoints.

    Args:
        network: Network name.
        instance: Node instance name.
        state_id: State identifier (default: "head").

    Returns:
        Response with finalized, current_justified, previous_justified checkpoints.
    """
    return beacon_get(
        network, instance, f"/eth/v1/beacon/states/{state_id}/finality_checkpoints"
    )


def get_config_spec(network: str, instance: str) -> dict[str, Any]:
    """Get chain config spec.

    Args:
        network: Network name.
        instance: Node instance name.

    Returns:
        Response with full chain configuration parameters.
    """
    return beacon_get(network, instance, "/eth/v1/config/spec")


def get_fork_schedule(network: str, instance: str) -> dict[str, Any]:
    """Get fork schedule.

    Args:
        network: Network name.
        instance: Node instance name.

    Returns:
        Response with ordered list of fork objects.
    """
    return beacon_get(network, instance, "/eth/v1/config/fork_schedule")


def get_deposit_contract(network: str, instance: str) -> dict[str, Any]:
    """Get deposit contract info.

    Args:
        network: Network name.
        instance: Node instance name.

    Returns:
        Response with chain_id and deposit contract address.
    """
    return beacon_get(network, instance, "/eth/v1/config/deposit_contract")


# ---------------------------------------------------------------------------
# Curated Execution Node (EL) functions
# ---------------------------------------------------------------------------


def eth_block_number(network: str, instance: str) -> int:
    """Get latest block number.

    Args:
        network: Network name.
        instance: Node instance name.

    Returns:
        Latest block number as integer.
    """
    result = execution_rpc(network, instance, "eth_blockNumber")

    return int(result, 16)


def eth_syncing(network: str, instance: str) -> dict[str, Any] | bool:
    """Get EL sync status.

    Args:
        network: Network name.
        instance: Node instance name.

    Returns:
        False if not syncing, or dict with sync progress details.
    """
    return execution_rpc(network, instance, "eth_syncing")


def eth_chain_id(network: str, instance: str) -> int:
    """Get chain ID.

    Args:
        network: Network name.
        instance: Node instance name.

    Returns:
        Chain ID as integer.
    """
    result = execution_rpc(network, instance, "eth_chainId")

    return int(result, 16)


def eth_get_block_by_number(
    network: str,
    instance: str,
    block: str = "latest",
    full_tx: bool = False,
) -> dict[str, Any]:
    """Get block by number.

    Args:
        network: Network name.
        instance: Node instance name.
        block: Block number as hex string or tag ("latest", "earliest", "pending").
        full_tx: If True, return full transaction objects; if False, just hashes.

    Returns:
        Block object.
    """
    return execution_rpc(network, instance, "eth_getBlockByNumber", [block, full_tx])


def net_peer_count(network: str, instance: str) -> int:
    """Get EL peer count.

    Args:
        network: Network name.
        instance: Node instance name.

    Returns:
        Number of connected peers as integer.
    """
    result = execution_rpc(network, instance, "net_peerCount")

    return int(result, 16)


def web3_client_version(network: str, instance: str) -> str:
    """Get EL client version string.

    Args:
        network: Network name.
        instance: Node instance name.

    Returns:
        Client version string.
    """
    return execution_rpc(network, instance, "web3_clientVersion")
