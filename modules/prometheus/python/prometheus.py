"""Thin Prometheus wrappers over server operations."""

from __future__ import annotations

from typing import Any

from ethpandaops import _runtime


def list_datasources() -> list[dict[str, Any]]:
    data = _runtime.invoke_data("prometheus.list_datasources") or {}
    datasources = data.get("datasources", [])
    return datasources if isinstance(datasources, list) else []


def query(
    instance_name: str,
    promql: str,
    time: str | None = None,
) -> dict[str, Any]:
    return _runtime.invoke_json_data(
        "prometheus.query",
        {
            "datasource": instance_name,
            "query": promql,
            "time": time,
        },
    )


def query_range(
    instance_name: str,
    promql: str,
    step: str,
    start: str | None = None,
    end: str | None = None,
) -> dict[str, Any]:
    return _runtime.invoke_json_data(
        "prometheus.query_range",
        {
            "datasource": instance_name,
            "query": promql,
            "start": start,
            "end": end,
            "step": step,
        },
    )


def get_labels(
    instance_name: str,
    start: str | None = None,
    end: str | None = None,
) -> list[str]:
    data = _runtime.invoke_json_data(
        "prometheus.get_labels",
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
    contains: str | None = None,
    limit: int | None = None,
) -> list[str]:
    """Return label values, optionally filtered locally for concise discovery."""
    data = _runtime.invoke_json_data(
        "prometheus.get_label_values",
        {
            "datasource": instance_name,
            "label": label,
            "start": start,
            "end": end,
        },
    )
    values = data if isinstance(data, list) else []

    if contains:
        needle = contains.lower()
        values = [value for value in values if needle in str(value).lower()]

    if limit is not None:
        values = values[:limit]

    return values
