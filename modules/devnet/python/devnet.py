"""Call devnet service APIs (spamoor, assertoor) through the proxy relay.

The proxy holds the credential the services require for writes, so code here
never handles a devnet token or cookie. ``path`` is the service's own API
route, e.g. ``/api/spammers`` (spamoor) or ``/api/v1/test_runs`` (assertoor);
``network`` is a cartographoor network name such as ``glamsterdam-devnet-8``.
"""

from __future__ import annotations

import json as _json
import os
from typing import Any

from ethpandaops import _runtime


def list_services() -> list[str]:
    """Devnet services the proxy relays for you, e.g. ['assertoor', 'spamoor']."""
    raw = os.environ.get("ETHPANDAOPS_DEVNET_SERVICES", "").strip()
    return _json.loads(raw) if raw else []


def _require_relay() -> None:
    if not list_services():
        raise ValueError(
            "No devnet services are relayed for you: the proxy has no devnet_services "
            "configured, or your account is not in their allowed_orgs."
        )


def list_networks() -> list[dict[str, Any]]:
    """Active networks running spamoor or assertoor.

    Each entry has keys: name, type, and extra.services mapping each service
    to {url, relayed}; relayed is True when this proxy relays the service.
    """
    _require_relay()
    data = _runtime.invoke_data("devnet.list_networks")
    return data.get("networks", [])


def request(
    network: str,
    service: str,
    method: str,
    path: str,
    json: Any = None,
    params: dict[str, Any] | None = None,
) -> Any:
    """Call ``method path`` on ``service`` of ``network`` through the relay.

    JSON responses are parsed; other content returns text. Raises ValueError
    on a non-2xx response with the service's error message.
    """
    _require_relay()
    args: dict[str, Any] = {
        "network": network,
        "service": service,
        "method": method,
        "path": path,
    }
    if json is not None:
        args["body"] = json
    if params:
        args["query"] = params

    body, content_type = _runtime._invoke_bytes("devnet.request", args)
    if "application/json" in content_type:
        return _json.loads(body) if body.strip() else None
    return body.decode("utf-8", errors="replace")
