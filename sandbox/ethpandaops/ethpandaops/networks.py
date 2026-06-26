"""Network discovery: the authoritative Cartographoor inventory.

This module is the easy way to get data about the Ethereum networks
ethpandaops tracks — mainnet, public testnets (hoodi, ...), and devnets —
including their forks, client images and versions, service endpoints (RPC,
beacon, Dora, Forky, ...), genesis timing, and blob schedule. Devnets are one
filtered view via devnets() or list(devnets_only=True).

Example usage:
    from ethpandaops import networks

    # List all active networks
    for net in networks.list():
        print(net["id"], net["chain_id"], net["devnet_group"])

    # ...or just the devnets
    for net in networks.devnets():
        print(net["id"], net["chain_id"], net["devnet_group"])

    # Full detail for any network (mainnet, a testnet, or a devnet)
    detail = networks.get("fusaka-devnet-3")
    print(detail["genesis_time"], detail["chain_id"])

    # Just the bits you need
    networks.forks("fusaka-devnet-3")      # {'consensus': {...}, 'execution': {...}}
    networks.clients("fusaka-devnet-3")    # [{'name': 'geth', 'version': '...'}, ...]
    networks.endpoints("fusaka-devnet-3")  # {'rpc': '...', 'beacon': '...', 'dora': '...'}
    networks.genesis("fusaka-devnet-3")    # {'genesis_time': ..., 'chain_id': ...}

    # Devnet families
    networks.groups()                      # ['fusaka', 'pectra', ...]
    networks.group("fusaka")               # [{'id': 'fusaka-devnet-3', ...}, ...]
"""

from __future__ import annotations

from typing import Any

from ethpandaops._runtime import invoke_data


def list(active: bool = True, devnets_only: bool = False) -> list[dict[str, Any]]:
    """List networks from the Cartographoor inventory.

    Args:
        active: When True (default), only active networks. Set False to include
                inactive ones.
        devnets_only: When True, only ethpandaops devnets (excludes mainnet,
                      public testnets, etc.).

    Returns:
        List of dicts, each with 'id', 'name', 'chain_id', 'status',
        'is_devnet', and 'devnet_group' keys.
    """
    data = invoke_data(
        "network.list", {"active": active, "devnets_only": devnets_only}
    )

    return data.get("networks", [])


def devnets(active: bool = True) -> list[dict[str, Any]]:
    """List devnets only. Sugar for list(devnets_only=True).

    Returns:
        List of devnet dicts (see list()).
    """
    return list(active=active, devnets_only=True)


def get(network: str) -> dict[str, Any]:
    """Get the full curated detail for a network or devnet.

    Args:
        network: Network id (e.g., "fusaka-devnet-3"). Use list() for ids.

    Returns:
        Dict with 'id', 'name', 'chain_id', 'status', 'is_devnet',
        'devnet_group', 'genesis_time', 'genesis_delay', 'forks', 'clients',
        'tools', 'endpoints', 'blob_schedule', 'links', 'node_inventory_url'.
    """
    return invoke_data("network.get", {"network": network})


def forks(network: str) -> dict[str, Any]:
    """Get the fork schedule for a network.

    Returns:
        Dict with 'consensus' and 'execution' keys, each mapping fork name to
        its activation (epoch/timestamp for consensus, block/timestamp for
        execution). Empty dict if the network advertises no forks.
    """
    return get(network).get("forks") or {}


def clients(network: str) -> list[dict[str, str]]:
    """Get the client images (name + version) running on a network.

    Returns:
        List of dicts, each with 'name' and 'version' keys.
    """
    return get(network).get("clients", [])


def endpoints(network: str) -> dict[str, str]:
    """Get the service endpoints for a network.

    Returns:
        Dict of non-empty service URLs keyed by role, e.g. 'rpc', 'beacon',
        'explorer', 'dora', 'forky', 'tracoor', 'cbt', 'faucet',
        'checkpoint_sync', 'blobscan'.
    """
    return get(network).get("endpoints") or {}


def genesis(network: str) -> dict[str, Any]:
    """Get genesis timing and chain identity for a network.

    Returns:
        Dict with 'genesis_time', 'genesis_delay', and 'chain_id' keys.
    """
    detail = get(network)

    return {
        "genesis_time": detail.get("genesis_time"),
        "genesis_delay": detail.get("genesis_delay"),
        "chain_id": detail.get("chain_id"),
    }


def groups() -> list[str]:
    """List active devnet group (family) names, e.g. ['fusaka', 'pectra'].

    Returns:
        Sorted list of group names.
    """
    data = invoke_data("network.list", {"active": True})

    return data.get("groups", [])


def group(name: str) -> list[dict[str, Any]]:
    """List all networks in a devnet group (family).

    Args:
        name: Group name (e.g., "fusaka"). Use groups() for names.

    Returns:
        List of network dicts (see list()).
    """
    data = invoke_data("network.group", {"group": name})

    return data.get("networks", [])
