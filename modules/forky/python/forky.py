"""Thin Forky wrappers over server operations.

Forky captures fork-choice snapshots ("frames") from beacon nodes. Each frame
holds the metadata of the capture (node, slot, epoch, labels, consensus
client, event source) plus the full fork-choice dump
(/eth/v1/debug/fork_choice) at that moment.
"""

from __future__ import annotations

import os
from typing import Any

from ethpandaops import _runtime


def _require_forky_available() -> None:
    if not os.environ.get("ETHPANDAOPS_FORKY_NETWORKS", "").strip():
        raise ValueError("Forky is not enabled or no Forky instances are available.")


def _filter_args(
    node: str | None = None,
    slot: int | None = None,
    epoch: int | None = None,
    labels: list[str] | None = None,
    consensus_client: str | None = None,
    event_source: str | None = None,
    before: str | None = None,
    after: str | None = None,
    offset: int = 0,
    limit: int = 100,
) -> dict[str, Any]:
    args: dict[str, Any] = {"offset": offset, "limit": limit}
    if node is not None:
        args["node"] = node
    if slot is not None:
        args["slot"] = slot
    if epoch is not None:
        args["epoch"] = epoch
    if labels:
        args["labels"] = labels
    if consensus_client is not None:
        args["consensus_client"] = consensus_client
    if event_source is not None:
        args["event_source"] = event_source
    if before is not None:
        args["before"] = before
    if after is not None:
        args["after"] = after
    return args


def list_networks() -> list[dict[str, Any]]:
    """List networks with Forky instances.

    Each entry has keys: name, url, type.
    """
    _require_forky_available()
    data = _runtime.invoke_data("forky.list_networks")
    return data.get("networks", [])


def get_base_url(network: str) -> str:
    _require_forky_available()
    data = _runtime.invoke_data("forky.get_base_url", {"network": network})
    return data.get("base_url", "")


def get_now(network: str) -> dict[str, Any]:
    """Get the network's current wall-clock slot and epoch."""
    _require_forky_available()
    return _runtime.invoke_data("forky.get_now", {"network": network})


def get_spec(network: str) -> dict[str, Any]:
    """Get the network name and chain spec (seconds_per_slot, slots_per_epoch, genesis_time)."""
    _require_forky_available()
    return _runtime.invoke_data("forky.get_spec", {"network": network})


def list_frames(
    network: str,
    node: str | None = None,
    slot: int | None = None,
    epoch: int | None = None,
    labels: list[str] | None = None,
    consensus_client: str | None = None,
    event_source: str | None = None,
    before: str | None = None,
    after: str | None = None,
    offset: int = 0,
    limit: int = 100,
) -> dict[str, Any]:
    """List fork-choice frame metadata matching a filter.

    All filter fields are optional and combined with AND. ``labels`` requires a
    frame to carry every listed label. ``event_source`` is one of
    ``beacon_node``, ``xatu_polling``, ``xatu_reorg_event``. ``before``/``after``
    are RFC 3339 timestamps compared against the frame's fetch time.

    Returns {"frames": [metadata, ...], "total": int}.
    """
    _require_forky_available()
    args = _filter_args(
        node, slot, epoch, labels, consensus_client, event_source, before, after, offset, limit
    )
    args["network"] = network
    return _runtime.invoke_data("forky.list_frames", args)


def get_frame(network: str, frame_id: str) -> dict[str, Any]:
    """Get a full fork-choice frame by ID.

    Returns {"metadata": {...}, "data": {justified_checkpoint, finalized_checkpoint,
    fork_choice_nodes}}. Slots, epochs, and weights inside ``data`` are strings,
    following beacon API convention.
    """
    _require_forky_available()
    payload = _runtime.invoke_json(
        "forky.get_frame",
        {"network": network, "frame_id": frame_id},
    )
    if not isinstance(payload, dict):
        return {}
    data = payload.get("data")
    if not isinstance(data, dict):
        return {}
    frame = data.get("frame")
    return frame if isinstance(frame, dict) else {}


def list_nodes(network: str, **filters: Any) -> dict[str, Any]:
    """List distinct node names with frames matching a filter.

    Accepts the same filters as list_frames. Returns {"nodes": [...], "total": int}.
    """
    _require_forky_available()
    args = _filter_args(**filters)
    args["network"] = network
    return _runtime.invoke_data("forky.list_nodes", args)


def list_slots(network: str, **filters: Any) -> dict[str, Any]:
    """List distinct wall-clock slots with frames matching a filter.

    Accepts the same filters as list_frames. Returns {"slots": [...], "total": int}.
    """
    _require_forky_available()
    args = _filter_args(**filters)
    args["network"] = network
    return _runtime.invoke_data("forky.list_slots", args)


def list_epochs(network: str, **filters: Any) -> dict[str, Any]:
    """List distinct wall-clock epochs with frames matching a filter.

    Accepts the same filters as list_frames. Returns {"epochs": [...], "total": int}.
    """
    _require_forky_available()
    args = _filter_args(**filters)
    args["network"] = network
    return _runtime.invoke_data("forky.list_epochs", args)


def list_labels(network: str, **filters: Any) -> dict[str, Any]:
    """List distinct frame labels matching a filter.

    Accepts the same filters as list_frames. Returns {"labels": [...], "total": int}.
    """
    _require_forky_available()
    args = _filter_args(**filters)
    args["network"] = network
    return _runtime.invoke_data("forky.list_labels", args)


def link_frame(network: str, frame_id: str) -> str:
    """Deep link to a frame snapshot in the Forky UI."""
    _require_forky_available()
    data = _runtime.invoke_data(
        "forky.link_frame",
        {"network": network, "frame_id": frame_id},
    )
    return data.get("url", "")


def link_node(network: str, node: str) -> str:
    """Deep link to a node's live view in the Forky UI."""
    _require_forky_available()
    data = _runtime.invoke_data(
        "forky.link_node",
        {"network": network, "node": node},
    )
    return data.get("url", "")
