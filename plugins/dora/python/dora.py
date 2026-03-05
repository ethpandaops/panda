"""Dora beacon chain explorer access.

Query the Dora beacon chain explorer API and generate deep links.
Network URLs are discovered from cartographoor via environment variables.

Example:
    from ethpandaops import dora

    networks = dora.list_networks()
    overview = dora.get_network_overview("hoodi")
    link = dora.link_validator("hoodi", "12345")
"""

import json
import os
from typing import Any

import httpx

_NETWORKS: dict[str, str] | None = None
_TIMEOUT = httpx.Timeout(connect=5.0, read=30.0, write=10.0, pool=5.0)


def _load_networks() -> dict[str, str]:
    """Load network -> Dora URL mapping from environment."""
    global _NETWORKS
    if _NETWORKS is not None:
        return _NETWORKS

    raw = os.environ.get("ETHPANDAOPS_DORA_NETWORKS", "")
    if not raw:
        _NETWORKS = {}
        return _NETWORKS

    try:
        _NETWORKS = json.loads(raw)
    except json.JSONDecodeError as e:
        raise ValueError(f"Invalid ETHPANDAOPS_DORA_NETWORKS JSON: {e}") from e

    return _NETWORKS


def _get_url(network: str) -> str:
    """Get Dora base URL for a network."""
    networks = _load_networks()
    if network not in networks:
        raise ValueError(f"Unknown network '{network}'. Available: {list(networks.keys())}")
    return networks[network]


def _api_get(network: str, path: str, params: dict[str, Any] | None = None) -> dict[str, Any]:
    """Make GET request to Dora API."""
    url = f"{_get_url(network)}{path}"
    with httpx.Client(timeout=_TIMEOUT) as client:
        resp = client.get(url, params=params)
        resp.raise_for_status()
        return resp.json()


# Network discovery


def list_networks() -> list[dict[str, str]]:
    """List networks with Dora explorers."""
    return [{"name": n, "dora_url": u} for n, u in sorted(_load_networks().items())]


def get_base_url(network: str) -> str:
    """Get Dora base URL for a network."""
    return _get_url(network)


# API queries


def get_network_overview(network: str) -> dict[str, Any]:
    """Get network overview: current epoch, slot, validator counts."""
    try:
        data = _api_get(network, "/api/v1/epoch/head").get("data", {})
    except Exception:
        data = _api_get(network, "/api/v1/epoch/latest").get("data", {})
    epoch = data.get("epoch", 0)
    result = {
        "current_epoch": epoch,
        "current_slot": epoch * 32,
        "finalized": data.get("finalized", False),
        "participation_rate": data.get("globalparticipationrate", 0.0),
    }
    if vi := data.get("validatorinfo"):
        result.update({
            "active_validator_count": vi.get("active", 0),
            "total_validator_count": vi.get("total", 0),
            "pending_validator_count": vi.get("pending", 0),
            "exited_validator_count": vi.get("exited", 0),
        })
    return result


def get_validator(network: str, index_or_pubkey: str) -> dict[str, Any]:
    """Get validator details by index or public key."""
    return _api_get(network, f"/api/v1/validator/{index_or_pubkey}").get("data", {})


def get_validators(network: str, status: str | None = None, limit: int = 100) -> list[dict[str, Any]]:
    """Get validators with optional status filter."""
    params: dict[str, Any] = {"limit": limit}
    if status:
        params["status"] = status
    return _api_get(network, "/api/v1/validators", params).get("data", [])


def get_slot(network: str, slot_or_hash: str) -> dict[str, Any]:
    """Get slot details by number or block hash."""
    return _api_get(network, f"/api/v1/slot/{slot_or_hash}").get("data", {})


def get_epoch(network: str, epoch: int) -> dict[str, Any]:
    """Get epoch summary."""
    return _api_get(network, f"/api/v1/epoch/{epoch}").get("data", {})


# Deep links


def link_validator(network: str, index_or_pubkey: str) -> str:
    """Generate link to validator page."""
    return f"{_get_url(network)}/validator/{index_or_pubkey}"


def link_slot(network: str, slot_or_hash: str) -> str:
    """Generate link to slot page."""
    return f"{_get_url(network)}/slot/{slot_or_hash}"


def link_epoch(network: str, epoch: int) -> str:
    """Generate link to epoch page."""
    return f"{_get_url(network)}/epoch/{epoch}"


def link_address(network: str, address: str) -> str:
    """Generate link to address page."""
    return f"{_get_url(network)}/address/{address}"


def link_block(network: str, number_or_hash: str) -> str:
    """Generate link to execution block page."""
    return f"{_get_url(network)}/block/{number_or_hash}"
