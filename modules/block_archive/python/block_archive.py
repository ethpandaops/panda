"""Thin Block Archive wrappers over server operations."""

from __future__ import annotations

import os
from typing import Any

from ethpandaops import _runtime


def _require_block_archive_available() -> None:
    if not os.environ.get("ETHPANDAOPS_BLOCK_ARCHIVE_URL", "").strip():
        raise ValueError("Block Archive is not enabled.")


def _coerce_slot(slot: Any) -> int:
    if isinstance(slot, bool):
        raise TypeError("slot must be an integer, got bool")
    if isinstance(slot, int):
        return slot
    if isinstance(slot, float):
        if not slot.is_integer():
            raise ValueError(f"slot must be an integer, got {slot!r}")
        return int(slot)
    if isinstance(slot, str):
        return int(slot, 10)
    raise TypeError(f"slot must be an integer, got {type(slot).__name__}")


def list_networks() -> list[str]:
    """Return the networks the archive serves."""
    _require_block_archive_available()
    data = _runtime.invoke_data("block_archive.list_networks")
    networks = data.get("networks", [])
    return [name for name in networks if isinstance(name, str)]


def get_base_url() -> str:
    """Return the block-archiver base URL."""
    _require_block_archive_available()
    data = _runtime.invoke_data("block_archive.get_base_url")
    return data.get("base_url", "")


def download_ssz(network: str, slot: int, block_root: str) -> bytes:
    """Fetch the SSZ-encoded SignedBeaconBlock as raw bytes."""
    _require_block_archive_available()
    body, _ = _runtime._invoke_bytes(  # noqa: SLF001
        "block_archive.download_ssz",
        {"network": network, "slot": _coerce_slot(slot), "block_root": block_root},
    )
    return body


def get_block_json(network: str, slot: int, block_root: str) -> dict[str, Any]:
    """Get the decoded JSON representation of the SignedBeaconBlock."""
    _require_block_archive_available()
    payload = _runtime.invoke_json(
        "block_archive.get_block_json",
        {"network": network, "slot": _coerce_slot(slot), "block_root": block_root},
    )
    if not isinstance(payload, dict):
        return {}
    return payload


def link(network: str, slot: int, block_root: str) -> str:
    """Build a browser link to view the block in the archive UI."""
    _require_block_archive_available()
    data = _runtime.invoke_data(
        "block_archive.link",
        {"network": network, "slot": _coerce_slot(slot), "block_root": block_root},
    )
    return data.get("url", "")
