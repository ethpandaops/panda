"""Shared runtime for thin ethpandaops module wrappers."""

from __future__ import annotations

import csv
import io
import json
import os
from typing import Any

import httpx
import pandas as pd

_API_URL = os.environ.get("ETHPANDAOPS_API_URL", "")
_API_TOKEN = os.environ.get("ETHPANDAOPS_API_TOKEN", "")
_API_UDS = os.environ.get("ETHPANDAOPS_API_UDS", "")
_SESSION_ID = os.environ.get("ETHPANDAOPS_SESSION_ID", "")


def _session_params() -> dict[str, str]:
    """Return the session_id query param when running inside a session."""
    return {"session_id": _SESSION_ID} if _SESSION_ID else {}


def _check_api_config() -> None:
    if not _API_URL or not _API_TOKEN:
        raise ValueError(
            "Server API not configured. ETHPANDAOPS_API_URL and ETHPANDAOPS_API_TOKEN are required."
        )


def _get_client(write_timeout: float = 60.0) -> httpx.Client:
    _check_api_config()

    # Direct backend: no network route, so reach the server over a unix socket
    # (httpx dials it by path; _API_URL is then just the HTTP authority).
    transport = httpx.HTTPTransport(uds=_API_UDS) if _API_UDS else None

    return httpx.Client(
        base_url=_API_URL,
        headers={"Authorization": f"Bearer {_API_TOKEN}"},
        timeout=httpx.Timeout(connect=5.0, read=300.0, write=write_timeout, pool=5.0),
        transport=transport,
    )


def _invoke_bytes(
    operation: str, args: dict[str, Any] | None = None
) -> tuple[bytes, str]:
    payload = {"args": args or {}}
    with _get_client() as client:
        response = client.post(f"/api/v1/runtime/operations/{operation}", json=payload)
        body = response.read()
        if not response.is_success:
            raise ValueError(
                f"Operation {operation} failed (HTTP {response.status_code}): "
                f"{body.decode('utf-8', errors='replace').strip()}"
            )

        content_type = response.headers.get("content-type", "")

    return body, content_type


def _decode_json(body: bytes) -> Any:
    if not body.strip():
        return {}

    try:
        return json.loads(body)
    except json.JSONDecodeError as exc:
        raise ValueError(
            "Unsupported server response shape. "
            "The server must implement /api/v1/runtime/operations/*."
        ) from exc


def invoke(operation: str, args: dict[str, Any] | None = None) -> dict[str, Any]:
    body, _ = _invoke_bytes(operation, args)
    data = _decode_json(body)

    if not isinstance(data, dict) or "kind" not in data:
        raise ValueError(
            "Unsupported server response shape. "
            "The server must implement /api/v1/runtime/operations/*."
        )

    return data


def invoke_json(operation: str, args: dict[str, Any] | None = None) -> Any:
    body, _ = _invoke_bytes(operation, args)
    return _decode_json(body)


def invoke_json_data(operation: str, args: dict[str, Any] | None = None) -> Any:
    payload = invoke_json(operation, args)
    if not isinstance(payload, dict):
        raise ValueError(f"Operation {operation} did not return a JSON object")

    return payload.get("data")


def invoke_tsv_dataframe(
    operation: str, args: dict[str, Any] | None = None
) -> pd.DataFrame:
    body, _ = _invoke_bytes(operation, args)
    text = body.decode("utf-8")
    if not text.strip():
        return pd.DataFrame()

    return pd.read_csv(io.StringIO(text), sep="\t")


def invoke_tsv_rows(
    operation: str, args: dict[str, Any] | None = None
) -> tuple[list[tuple[str, ...]], list[str]]:
    body, _ = _invoke_bytes(operation, args)
    text = body.decode("utf-8")
    if not text.strip():
        return [], []

    reader = csv.reader(io.StringIO(text), delimiter="\t")
    records = list(reader)
    if not records:
        return [], []

    columns = records[0]
    rows = [tuple(row) for row in records[1:]]
    return rows, columns


def invoke_data(operation: str, args: dict[str, Any] | None = None) -> Any:
    response = invoke(operation, args)
    if response.get("kind") != "object":
        raise ValueError(f"Operation {operation} did not return an object result")

    return response.get("data")
