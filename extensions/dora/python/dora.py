"""Thin Dora wrappers over proxy operations."""

from __future__ import annotations

import os
from typing import Any

from ethpandaops import _runtime


def _require_dora_available() -> None:
    if not os.environ.get("ETHPANDAOPS_DORA_NETWORKS", "").strip():
        raise ValueError("Dora is not enabled or no Dora explorers are available.")


def list_networks() -> list[dict[str, str]]:
    _require_dora_available()
    data = _runtime.invoke_data("dora.list_networks")
    return data.get("networks", [])


def get_base_url(network: str) -> str:
    _require_dora_available()
    data = _runtime.invoke_data("dora.get_base_url", {"network": network})
    return data.get("base_url", "")


def get_network_overview(network: str) -> dict[str, Any]:
    _require_dora_available()
    return _runtime.invoke_data("dora.get_network_overview", {"network": network})


def get_validator(network: str, index_or_pubkey: str) -> dict[str, Any]:
    _require_dora_available()
    payload = _runtime.invoke_json(
        "dora.get_validator",
        {"network": network, "index_or_pubkey": index_or_pubkey},
    )
    if not isinstance(payload, dict):
        return {}
    data = payload.get("data")
    return data if isinstance(data, dict) else {}


def get_validators(
    network: str, status: str | None = None, limit: int = 100
) -> list[dict[str, Any]]:
    _require_dora_available()
    payload = _runtime.invoke_json(
        "dora.get_validators",
        {"network": network, "status": status, "limit": limit},
    )
    if not isinstance(payload, dict):
        return []
    data = payload.get("data")
    return data if isinstance(data, list) else []


def get_slot(network: str, slot_or_hash: str) -> dict[str, Any]:
    _require_dora_available()
    payload = _runtime.invoke_json(
        "dora.get_slot",
        {"network": network, "slot_or_hash": slot_or_hash},
    )
    if not isinstance(payload, dict):
        return {}
    data = payload.get("data")
    return data if isinstance(data, dict) else {}


def get_epoch(network: str, epoch: int) -> dict[str, Any]:
    _require_dora_available()
    payload = _runtime.invoke_json(
        "dora.get_epoch",
        {"network": network, "epoch": str(epoch)},
    )
    if not isinstance(payload, dict):
        return {}
    data = payload.get("data")
    return data if isinstance(data, dict) else {}


def link_validator(network: str, index_or_pubkey: str) -> str:
    _require_dora_available()
    data = _runtime.invoke_data(
        "dora.link_validator",
        {"network": network, "index_or_pubkey": index_or_pubkey},
    )
    return data.get("url", "")


def link_slot(network: str, slot_or_hash: str) -> str:
    _require_dora_available()
    data = _runtime.invoke_data(
        "dora.link_slot",
        {"network": network, "slot_or_hash": slot_or_hash},
    )
    return data.get("url", "")


def link_epoch(network: str, epoch: int) -> str:
    _require_dora_available()
    data = _runtime.invoke_data(
        "dora.link_epoch",
        {"network": network, "epoch": str(epoch)},
    )
    return data.get("url", "")


def link_address(network: str, address: str) -> str:
    _require_dora_available()
    data = _runtime.invoke_data(
        "dora.link_address",
        {"network": network, "address": address},
    )
    return data.get("url", "")


def link_block(network: str, number_or_hash: str) -> str:
    _require_dora_available()
    data = _runtime.invoke_data(
        "dora.link_block",
        {"network": network, "number_or_hash": number_or_hash},
    )
    return data.get("url", "")
