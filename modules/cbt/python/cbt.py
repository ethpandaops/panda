"""Thin CBT wrappers over server operations.

CBT manages the ClickHouse transformation pipeline that builds the
pre-aggregated analytics tables (``fct_*``, ``int_*``, ``dim_*``). Model IDs
are ``database.table`` (e.g. ``mainnet.fct_block``). Incremental
transformations track processed ``(position, interval)`` ranges where the
position unit depends on the model's interval type — unix seconds for
slot-type models, block number for block-type models (see
``get_interval_types``).
"""

from __future__ import annotations

import os
from typing import Any

from ethpandaops import _runtime


def _require_cbt_available() -> None:
    if not os.environ.get("ETHPANDAOPS_CBT_NETWORKS", "").strip():
        raise ValueError("CBT is not enabled or no CBT instances are available.")


def _items(body: Any, key: str) -> list[dict[str, Any]]:
    if isinstance(body, dict):
        return body.get(key) or []
    return body or []


def list_networks() -> list[dict[str, Any]]:
    """List networks with CBT instances.

    Each entry has keys: name, url, type.
    """
    _require_cbt_available()
    data = _runtime.invoke_data("cbt.list_networks")
    return data.get("networks", [])


def list_models(
    network: str,
    type: str | None = None,
    database: str | None = None,
    search: str | None = None,
) -> list[dict[str, Any]]:
    """List all data models (type: "external" or "transformation")."""
    _require_cbt_available()
    args: dict[str, Any] = {"network": network}
    if type is not None:
        args["type"] = type
    if database is not None:
        args["database"] = database
    if search is not None:
        args["search"] = search
    return _items(_runtime.invoke_json("cbt.list_models", args), "models")


def list_external_models(
    network: str,
    database: str | None = None,
) -> list[dict[str, Any]]:
    """List external models (raw ClickHouse tables CBT reads from)."""
    _require_cbt_available()
    args: dict[str, Any] = {"network": network}
    if database is not None:
        args["database"] = database
    return _items(_runtime.invoke_json("cbt.list_external_models", args), "models")


def get_external_model(network: str, id: str) -> dict[str, Any]:
    """Get external model details by ID (database.table)."""
    _require_cbt_available()
    return _runtime.invoke_json(
        "cbt.get_external_model",
        {"network": network, "id": id},
    )


def get_external_bounds(
    network: str, id: str | None = None
) -> list[dict[str, Any]] | dict[str, Any]:
    """Get min/max available positions for external models.

    Without an ID, returns a list of bounds for every external model.
    With an ID, returns that model's bounds dict (min, max, scan timestamps).
    """
    _require_cbt_available()
    args: dict[str, Any] = {"network": network}
    if id is not None:
        args["id"] = id
        return _runtime.invoke_json("cbt.get_external_bounds", args)
    return _items(_runtime.invoke_json("cbt.get_external_bounds", args), "bounds")


def list_transformations(
    network: str,
    database: str | None = None,
    type: str | None = None,
    status: str | None = None,
) -> list[dict[str, Any]]:
    """List transformations (type: "incremental" or "scheduled")."""
    _require_cbt_available()
    args: dict[str, Any] = {"network": network}
    if database is not None:
        args["database"] = database
    if type is not None:
        args["type"] = type
    if status is not None:
        args["status"] = status
    return _items(_runtime.invoke_json("cbt.list_transformations", args), "models")


def get_transformation(network: str, id: str) -> dict[str, Any]:
    """Get transformation details (SQL content, schedules, dependencies)."""
    _require_cbt_available()
    return _runtime.invoke_json(
        "cbt.get_transformation",
        {"network": network, "id": id},
    )


def get_transformation_coverage(
    network: str, id: str | None = None
) -> list[dict[str, Any]] | dict[str, Any]:
    """Get processed (position, interval) ranges for transformations.

    Without an ID, returns a list of coverage summaries for every
    transformation. With an ID, returns that model's coverage dict with its
    ranges. A range (position=P, interval=N) means positions P..P+N are
    processed; multiple ranges mean there are gaps between them.
    """
    _require_cbt_available()
    args: dict[str, Any] = {"network": network}
    if id is not None:
        args["id"] = id
        return _runtime.invoke_json("cbt.get_transformation_coverage", args)
    return _items(
        _runtime.invoke_json("cbt.get_transformation_coverage", args), "coverage"
    )


def debug_coverage(network: str, id: str, position: int) -> dict[str, Any]:
    """Explain whether a transformation can process a given position.

    Returns the model's coverage around the position plus per-dependency
    bounds, gaps, and blocking reasons — use this to answer "why is this
    table missing data at position X".
    """
    _require_cbt_available()
    return _runtime.invoke_json(
        "cbt.debug_coverage",
        {"network": network, "id": id, "position": position},
    )


def get_scheduled_runs(
    network: str, id: str | None = None
) -> list[dict[str, Any]] | dict[str, Any]:
    """Get last-run times for scheduled transformations."""
    _require_cbt_available()
    args: dict[str, Any] = {"network": network}
    if id is not None:
        args["id"] = id
        return _runtime.invoke_json("cbt.get_scheduled_runs", args)
    return _items(_runtime.invoke_json("cbt.get_scheduled_runs", args), "runs")


def get_interval_types(network: str) -> dict[str, Any]:
    """Get interval type definitions mapping positions to human units."""
    _require_cbt_available()
    return _runtime.invoke_json(
        "cbt.get_interval_types",
        {"network": network},
    )


def link_model(network: str, id: str) -> str:
    """Deep link to a model in the CBT web UI."""
    _require_cbt_available()
    data = _runtime.invoke_data(
        "cbt.link_model",
        {"network": network, "id": id},
    )
    return data.get("url", "")
