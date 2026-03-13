"""Thin CBT wrappers over server operations."""

from __future__ import annotations

import os
from typing import Any

from ethpandaops import _runtime


def _require_cbt_available() -> None:
    if not os.environ.get("ETHPANDAOPS_CBT_NETWORKS", "").strip():
        raise ValueError("CBT is not enabled or no CBT instances are available.")


def list_networks() -> list[dict[str, str]]:
    _require_cbt_available()
    data = _runtime.invoke_data("cbt.list_networks")
    return data.get("networks", [])


def list_models(
    network: str,
    type: str | None = None,
    database: str | None = None,
    search: str | None = None,
) -> list[dict[str, Any]]:
    _require_cbt_available()
    args: dict[str, Any] = {"network": network}
    if type is not None:
        args["type"] = type
    if database is not None:
        args["database"] = database
    if search is not None:
        args["search"] = search
    return _runtime.invoke_json("cbt.list_models", args)


def list_external_models(
    network: str,
    database: str | None = None,
) -> list[dict[str, Any]]:
    _require_cbt_available()
    args: dict[str, Any] = {"network": network}
    if database is not None:
        args["database"] = database
    return _runtime.invoke_json("cbt.list_external_models", args)


def get_external_model(network: str, id: str) -> dict[str, Any]:
    _require_cbt_available()
    return _runtime.invoke_json(
        "cbt.get_external_model",
        {"network": network, "id": id},
    )


def get_external_bounds(
    network: str, id: str | None = None
) -> list[dict[str, Any]] | dict[str, Any]:
    _require_cbt_available()
    args: dict[str, Any] = {"network": network}
    if id is not None:
        args["id"] = id
    return _runtime.invoke_json("cbt.get_external_bounds", args)


def list_transformations(
    network: str,
    database: str | None = None,
    type: str | None = None,
    status: str | None = None,
) -> list[dict[str, Any]]:
    _require_cbt_available()
    args: dict[str, Any] = {"network": network}
    if database is not None:
        args["database"] = database
    if type is not None:
        args["type"] = type
    if status is not None:
        args["status"] = status
    return _runtime.invoke_json("cbt.list_transformations", args)


def get_transformation(network: str, id: str) -> dict[str, Any]:
    _require_cbt_available()
    return _runtime.invoke_json(
        "cbt.get_transformation",
        {"network": network, "id": id},
    )


def get_transformation_coverage(
    network: str, id: str | None = None
) -> list[dict[str, Any]] | dict[str, Any]:
    _require_cbt_available()
    args: dict[str, Any] = {"network": network}
    if id is not None:
        args["id"] = id
    return _runtime.invoke_json("cbt.get_transformation_coverage", args)


def get_scheduled_runs(
    network: str, id: str | None = None
) -> list[dict[str, Any]] | dict[str, Any]:
    _require_cbt_available()
    args: dict[str, Any] = {"network": network}
    if id is not None:
        args["id"] = id
    return _runtime.invoke_json("cbt.get_scheduled_runs", args)


def get_interval_types(network: str) -> dict[str, Any]:
    _require_cbt_available()
    return _runtime.invoke_json(
        "cbt.get_interval_types",
        {"network": network},
    )


def link_model(network: str, id: str) -> str:
    _require_cbt_available()
    data = _runtime.invoke_data(
        "cbt.link_model",
        {"network": network, "id": id},
    )
    return data.get("url", "")
