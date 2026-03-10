"""Thin Loki wrappers over server operations."""

from __future__ import annotations

from typing import Any

from ethpandaops import _runtime


def list_datasources() -> list[dict[str, Any]]:
    data = _runtime.invoke_data("loki.list_datasources")
    return data.get("datasources", [])


def query(
    instance_name: str,
    logql: str,
    limit: int = 100,
    start: str | None = None,
    end: str | None = None,
    direction: str = "backward",
) -> dict[str, Any]:
    data = _runtime.invoke_json_data(
        "loki.query",
        {
            "datasource": instance_name,
            "query": logql,
            "limit": limit,
            "start": start,
            "end": end,
            "direction": direction,
        },
    )
    return data if isinstance(data, dict) else {}


def query_instant(
    instance_name: str,
    logql: str,
    time: str | None = None,
    limit: int = 100,
    direction: str = "backward",
) -> dict[str, Any]:
    data = _runtime.invoke_json_data(
        "loki.query_instant",
        {
            "datasource": instance_name,
            "query": logql,
            "time": time,
            "limit": limit,
            "direction": direction,
        },
    )
    return data if isinstance(data, dict) else {}


def get_labels(
    instance_name: str,
    start: str | None = None,
    end: str | None = None,
) -> list[str]:
    data = _runtime.invoke_json_data(
        "loki.get_labels",
        {
            "datasource": instance_name,
            "start": start,
            "end": end,
        },
    )
    return data if isinstance(data, list) else []


def get_label_values(
    instance_name: str,
    label: str,
    start: str | None = None,
    end: str | None = None,
) -> list[str]:
    data = _runtime.invoke_json_data(
        "loki.get_label_values",
        {
            "datasource": instance_name,
            "label": label,
            "start": start,
            "end": end,
        },
    )
    return data if isinstance(data, list) else []
