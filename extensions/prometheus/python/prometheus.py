"""Thin Prometheus wrappers over proxy operations."""

from __future__ import annotations

from typing import Any

from ethpandaops import _runtime


def list_datasources() -> list[dict[str, Any]]:
    data = _runtime.invoke_data("prometheus.list_datasources")
    return data.get("datasources", [])


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
    start: str,
    end: str,
    step: str,
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


def get_labels(instance_name: str) -> list[str]:
    data = _runtime.invoke_json_data(
        "prometheus.get_labels",
        {"datasource": instance_name},
    )
    return data if isinstance(data, list) else []


def get_label_values(instance_name: str, label: str) -> list[str]:
    data = _runtime.invoke_json_data(
        "prometheus.get_label_values",
        {"datasource": instance_name, "label": label},
    )
    return data if isinstance(data, list) else []
