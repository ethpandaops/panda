"""Shared runtime for thin ethpandaops extension wrappers."""

from __future__ import annotations

import os
from typing import Any

import httpx
import pandas as pd

_PROXY_URL = os.environ.get("ETHPANDAOPS_PROXY_URL", "")
_PROXY_TOKEN = os.environ.get("ETHPANDAOPS_PROXY_TOKEN", "")


def _check_proxy_config() -> None:
    if not _PROXY_URL or not _PROXY_TOKEN:
        raise ValueError(
            "Proxy not configured. ETHPANDAOPS_PROXY_URL and ETHPANDAOPS_PROXY_TOKEN are required."
        )


def _get_client() -> httpx.Client:
    _check_proxy_config()
    return httpx.Client(
        base_url=_PROXY_URL,
        headers={"Authorization": f"Bearer {_PROXY_TOKEN}"},
        timeout=httpx.Timeout(connect=5.0, read=300.0, write=60.0, pool=5.0),
    )


def invoke(operation: str, args: dict[str, Any] | None = None) -> dict[str, Any]:
    payload = {"args": args or {}}
    with _get_client() as client:
        response = client.post(f"/api/v1/operations/{operation}", json=payload)
        if not response.is_success:
            raise ValueError(
                f"Operation {operation} failed (HTTP {response.status_code}): "
                f"{response.text.strip()}"
            )
        data = response.json()

    if not isinstance(data, dict) or "kind" not in data:
        raise ValueError(
            "Unsupported proxy response shape. "
            "The proxy must implement /api/v1/operations/*."
        )

    return data


def invoke_dataframe(operation: str, args: dict[str, Any] | None = None) -> pd.DataFrame:
    response = invoke(operation, args)
    if response.get("kind") != "table":
        raise ValueError(f"Operation {operation} did not return a table result")

    row_encoding = response.get("row_encoding", "object")
    if row_encoding != "object":
        raise ValueError(
            f"Operation {operation} returned row_encoding={row_encoding}, expected object"
        )

    rows = response.get("rows", [])
    columns = response.get("columns", [])
    if not rows:
        return pd.DataFrame(columns=columns)

    return pd.DataFrame(rows, columns=columns or None)


def invoke_raw_table(
    operation: str, args: dict[str, Any] | None = None
) -> tuple[list[tuple], list[str]]:
    response = invoke(operation, args)
    if response.get("kind") != "table":
        raise ValueError(f"Operation {operation} did not return a table result")

    row_encoding = response.get("row_encoding", "object")
    if row_encoding != "array":
        raise ValueError(
            f"Operation {operation} returned row_encoding={row_encoding}, expected array"
        )

    matrix = response.get("matrix", [])
    columns = response.get("columns", [])
    rows = [tuple(row) for row in matrix]
    return rows, columns
