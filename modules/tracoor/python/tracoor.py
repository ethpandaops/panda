"""Thin Tracoor wrappers over server operations.

Tracoor captures and archives consensus- and execution-layer forensics
artifacts from a fleet of nodes: beacon states and blocks (SSZ snapshots),
invalid ("bad") beacon blocks and blob sidecars rejected from gossip,
execution ``debug_traceBlock`` captures, and invalid execution blocks
(``debug_getBadBlocks``). Each capture row is metadata; the raw artifact is
fetched separately via ``get_download_url``.

Artifact type names (for ``list_unique_values``, ``get_download_url`` and
``link``): ``beacon_state``, ``beacon_block``, ``beacon_bad_block``,
``beacon_bad_blob``, ``execution_block_trace``, ``execution_bad_block``.
"""

from __future__ import annotations

import os
from typing import Any

from ethpandaops import _runtime

_INT_FIELDS = ("slot", "epoch", "index", "block_number")


def _require_tracoor_available() -> None:
    if not os.environ.get("ETHPANDAOPS_TRACOOR_NETWORKS", "").strip():
        raise ValueError("Tracoor is not enabled or no Tracoor instances are available.")


def _normalize(items: list[dict[str, Any]]) -> list[dict[str, Any]]:
    """Convert numeric string fields (uint64s arrive as strings) to ints."""
    for item in items:
        for key in _INT_FIELDS:
            value = item.get(key)
            if isinstance(value, str) and value.isdigit():
                item[key] = int(value)
    return items


def _filter_args(network: str, filters: dict[str, Any]) -> dict[str, Any]:
    args: dict[str, Any] = {"network": network}
    for key, value in filters.items():
        if value is not None:
            args[key] = value
    return args


def _list(network: str, artifact: str, filters: dict[str, Any]) -> list[dict[str, Any]]:
    _require_tracoor_available()
    args = _filter_args(network, filters)
    args["artifact"] = artifact
    body = _runtime.invoke_json("tracoor.list_artifacts", args)
    items: list[Any] = []
    if isinstance(body, dict):
        # The response has a single per-type key (e.g. {"beacon_states": [...]}).
        for value in body.values():
            if isinstance(value, list):
                items = value
                break
    return _normalize([i for i in items if isinstance(i, dict)])


def _count(network: str, artifact: str, filters: dict[str, Any]) -> int:
    _require_tracoor_available()
    args = _filter_args(network, filters)
    args["artifact"] = artifact
    data = _runtime.invoke_data("tracoor.count_artifacts", args)
    return int(data.get("count", 0))


def list_networks() -> list[dict[str, Any]]:
    """List networks with Tracoor instances.

    Each entry has keys: name, url, type.
    """
    _require_tracoor_available()
    data = _runtime.invoke_data("tracoor.list_networks")
    return data.get("networks", [])


def get_base_url(network: str) -> str:
    """Get the Tracoor base URL for a network."""
    _require_tracoor_available()
    data = _runtime.invoke_data("tracoor.get_base_url", {"network": network})
    return data.get("base_url", "")


def get_config(network: str) -> dict[str, Any]:
    """Get the instance's Ethereum network-config and tool settings."""
    _require_tracoor_available()
    return _runtime.invoke_json("tracoor.get_config", {"network": network})


def list_beacon_states(
    network: str,
    node: str | None = None,
    slot: int | None = None,
    epoch: int | None = None,
    state_root: str | None = None,
    node_version: str | None = None,
    beacon_implementation: str | None = None,
    before: str | None = None,
    after: str | None = None,
    id: str | None = None,
    offset: int = 0,
    limit: int = 100,
    order_by: str | None = None,
) -> list[dict[str, Any]]:
    """List captured beacon states (SSZ snapshots).

    Each entry: id, node, fetched_at, slot, epoch, state_root, node_version,
    network, beacon_implementation. before/after are RFC 3339 timestamps
    filtering on fetched_at; order_by defaults to "fetched_at DESC".
    """
    return _list(network, "beacon_state", {
        "node": node, "slot": slot, "epoch": epoch, "state_root": state_root,
        "node_version": node_version, "beacon_implementation": beacon_implementation,
        "before": before, "after": after, "id": id,
        "offset": offset, "limit": limit, "order_by": order_by,
    })


def count_beacon_states(
    network: str,
    node: str | None = None,
    slot: int | None = None,
    epoch: int | None = None,
    state_root: str | None = None,
    node_version: str | None = None,
    beacon_implementation: str | None = None,
    before: str | None = None,
    after: str | None = None,
) -> int:
    """Count captured beacon states matching a filter."""
    return _count(network, "beacon_state", {
        "node": node, "slot": slot, "epoch": epoch, "state_root": state_root,
        "node_version": node_version, "beacon_implementation": beacon_implementation,
        "before": before, "after": after,
    })


def list_beacon_blocks(
    network: str,
    node: str | None = None,
    slot: int | None = None,
    epoch: int | None = None,
    block_root: str | None = None,
    node_version: str | None = None,
    beacon_implementation: str | None = None,
    before: str | None = None,
    after: str | None = None,
    id: str | None = None,
    offset: int = 0,
    limit: int = 100,
    order_by: str | None = None,
) -> list[dict[str, Any]]:
    """List captured beacon blocks."""
    return _list(network, "beacon_block", {
        "node": node, "slot": slot, "epoch": epoch, "block_root": block_root,
        "node_version": node_version, "beacon_implementation": beacon_implementation,
        "before": before, "after": after, "id": id,
        "offset": offset, "limit": limit, "order_by": order_by,
    })


def count_beacon_blocks(
    network: str,
    node: str | None = None,
    slot: int | None = None,
    epoch: int | None = None,
    block_root: str | None = None,
    node_version: str | None = None,
    beacon_implementation: str | None = None,
    before: str | None = None,
    after: str | None = None,
) -> int:
    """Count captured beacon blocks matching a filter."""
    return _count(network, "beacon_block", {
        "node": node, "slot": slot, "epoch": epoch, "block_root": block_root,
        "node_version": node_version, "beacon_implementation": beacon_implementation,
        "before": before, "after": after,
    })


def list_beacon_bad_blocks(
    network: str,
    node: str | None = None,
    slot: int | None = None,
    epoch: int | None = None,
    block_root: str | None = None,
    node_version: str | None = None,
    beacon_implementation: str | None = None,
    before: str | None = None,
    after: str | None = None,
    id: str | None = None,
    offset: int = 0,
    limit: int = 100,
    order_by: str | None = None,
) -> list[dict[str, Any]]:
    """List invalid beacon blocks that nodes rejected from gossip."""
    return _list(network, "beacon_bad_block", {
        "node": node, "slot": slot, "epoch": epoch, "block_root": block_root,
        "node_version": node_version, "beacon_implementation": beacon_implementation,
        "before": before, "after": after, "id": id,
        "offset": offset, "limit": limit, "order_by": order_by,
    })


def count_beacon_bad_blocks(
    network: str,
    node: str | None = None,
    slot: int | None = None,
    epoch: int | None = None,
    block_root: str | None = None,
    node_version: str | None = None,
    beacon_implementation: str | None = None,
    before: str | None = None,
    after: str | None = None,
) -> int:
    """Count invalid beacon blocks matching a filter."""
    return _count(network, "beacon_bad_block", {
        "node": node, "slot": slot, "epoch": epoch, "block_root": block_root,
        "node_version": node_version, "beacon_implementation": beacon_implementation,
        "before": before, "after": after,
    })


def list_beacon_bad_blobs(
    network: str,
    node: str | None = None,
    slot: int | None = None,
    epoch: int | None = None,
    block_root: str | None = None,
    index: int | None = None,
    node_version: str | None = None,
    beacon_implementation: str | None = None,
    before: str | None = None,
    after: str | None = None,
    id: str | None = None,
    offset: int = 0,
    limit: int = 100,
    order_by: str | None = None,
) -> list[dict[str, Any]]:
    """List invalid blob sidecars that nodes rejected from gossip."""
    return _list(network, "beacon_bad_blob", {
        "node": node, "slot": slot, "epoch": epoch, "block_root": block_root,
        "index": index, "node_version": node_version,
        "beacon_implementation": beacon_implementation,
        "before": before, "after": after, "id": id,
        "offset": offset, "limit": limit, "order_by": order_by,
    })


def count_beacon_bad_blobs(
    network: str,
    node: str | None = None,
    slot: int | None = None,
    epoch: int | None = None,
    block_root: str | None = None,
    index: int | None = None,
    node_version: str | None = None,
    beacon_implementation: str | None = None,
    before: str | None = None,
    after: str | None = None,
) -> int:
    """Count invalid blob sidecars matching a filter."""
    return _count(network, "beacon_bad_blob", {
        "node": node, "slot": slot, "epoch": epoch, "block_root": block_root,
        "index": index, "node_version": node_version,
        "beacon_implementation": beacon_implementation,
        "before": before, "after": after,
    })


def list_execution_traces(
    network: str,
    node: str | None = None,
    block_number: int | None = None,
    block_hash: str | None = None,
    node_version: str | None = None,
    execution_implementation: str | None = None,
    before: str | None = None,
    after: str | None = None,
    id: str | None = None,
    offset: int = 0,
    limit: int = 100,
    order_by: str | None = None,
) -> list[dict[str, Any]]:
    """List execution-layer debug_traceBlock captures."""
    return _list(network, "execution_block_trace", {
        "node": node, "block_number": block_number, "block_hash": block_hash,
        "node_version": node_version,
        "execution_implementation": execution_implementation,
        "before": before, "after": after, "id": id,
        "offset": offset, "limit": limit, "order_by": order_by,
    })


def count_execution_traces(
    network: str,
    node: str | None = None,
    block_number: int | None = None,
    block_hash: str | None = None,
    node_version: str | None = None,
    execution_implementation: str | None = None,
    before: str | None = None,
    after: str | None = None,
) -> int:
    """Count execution trace captures matching a filter."""
    return _count(network, "execution_block_trace", {
        "node": node, "block_number": block_number, "block_hash": block_hash,
        "node_version": node_version,
        "execution_implementation": execution_implementation,
        "before": before, "after": after,
    })


def list_execution_bad_blocks(
    network: str,
    node: str | None = None,
    block_number: int | None = None,
    block_hash: str | None = None,
    block_extra_data: str | None = None,
    node_version: str | None = None,
    execution_implementation: str | None = None,
    before: str | None = None,
    after: str | None = None,
    id: str | None = None,
    offset: int = 0,
    limit: int = 100,
    order_by: str | None = None,
) -> list[dict[str, Any]]:
    """List invalid execution blocks (debug_getBadBlocks captures)."""
    return _list(network, "execution_bad_block", {
        "node": node, "block_number": block_number, "block_hash": block_hash,
        "block_extra_data": block_extra_data, "node_version": node_version,
        "execution_implementation": execution_implementation,
        "before": before, "after": after, "id": id,
        "offset": offset, "limit": limit, "order_by": order_by,
    })


def count_execution_bad_blocks(
    network: str,
    node: str | None = None,
    block_number: int | None = None,
    block_hash: str | None = None,
    block_extra_data: str | None = None,
    node_version: str | None = None,
    execution_implementation: str | None = None,
    before: str | None = None,
    after: str | None = None,
) -> int:
    """Count invalid execution blocks matching a filter."""
    return _count(network, "execution_bad_block", {
        "node": node, "block_number": block_number, "block_hash": block_hash,
        "block_extra_data": block_extra_data, "node_version": node_version,
        "execution_implementation": execution_implementation,
        "before": before, "after": after,
    })


def list_unique_values(
    network: str, artifact: str, fields: list[str]
) -> dict[str, list[str]]:
    """Distinct values for fields of an artifact type.

    Use this to discover which nodes, implementations, or networks have
    captures before filtering. Valid fields depend on the artifact type
    (e.g. node, network, beacon_implementation for beacon artifacts;
    execution_implementation, block_extra_data for execution ones).
    """
    _require_tracoor_available()
    body = _runtime.invoke_json(
        "tracoor.list_unique_values",
        {"network": network, "artifact": artifact, "fields": fields},
    )
    return {k: v for k, v in body.items() if isinstance(v, list) and v}


def get_download_url(network: str, artifact: str, id: str) -> str:
    """URL serving the raw stored artifact for a capture ID.

    The response is SSZ or JSON (Content-Type tells you which), possibly
    gzip-compressed, and may redirect to a presigned store URL.
    """
    _require_tracoor_available()
    data = _runtime.invoke_data(
        "tracoor.download_url",
        {"network": network, "artifact": artifact, "id": id},
    )
    return data.get("url", "")


def link(network: str, artifact: str, id: str | None = None) -> str:
    """Deep link to an artifact listing (or one capture) in the Tracoor UI."""
    _require_tracoor_available()
    args: dict[str, Any] = {"network": network, "artifact": artifact}
    if id is not None:
        args["id"] = id
    data = _runtime.invoke_data("tracoor.link_artifact", args)
    return data.get("url", "")
