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


def _series_name(metric: dict[str, Any]) -> str:
    """A short, stable identity for a metric series — most specific label first."""
    for key in ("instance", "pod", "container", "job"):
        value = metric.get(key)
        if value:
            return str(value)
    return "process"


def _restart_label(metric: dict[str, Any], instance: str) -> str:
    """A concise restart label. Prefers the client that actually restarted on ethpandaops
    devnets (execution_client for an EL process, consensus_client for a CL one), so the chart
    reads "ethrex restart" / "prysm restart" instead of the full instance name; falls back to
    the instance when those labels are absent."""
    job = metric.get("job", "")
    if job == "execution" and metric.get("execution_client"):
        return f"{metric['execution_client']} restart"
    if job in ("consensus_node", "consensus") and metric.get("consensus_client"):
        return f"{metric['consensus_client']} restart"
    return f"{instance} restart"


def restarts(
    instance_name: str,
    match: str = "",
    start: str | None = None,
    end: str | None = None,
    step: str = "60s",
) -> list[dict[str, Any]]:
    """Detect process restarts from ``process_start_time_seconds``.

    The gauge's *value* is the process start time (unix seconds), so every distinct
    value over the window is one start; the earliest one (already running when the
    window opened) is the baseline and is dropped. Returns event records
    ``[{"t", "label", "kind", "series"}]`` ready for ``ethpandaops.chartkit.events``.

    ``match`` is a PromQL label-selector body without braces, e.g. ``'job=~"ethrex.*"'``.
    Pass the same ``start``/``end`` as the chart you'll overlay these on.
    """
    query_text = "process_start_time_seconds"
    if match:
        query_text += "{" + match + "}"

    data = query_range(instance_name, query_text, step=step, start=start, end=end) or {}

    out: list[dict[str, Any]] = []
    for series in data.get("result", []):
        samples = series.get("values") or []
        if not samples:
            continue

        first_ts = float(samples[0][0])
        starts = sorted({round(float(value)) for _, value in samples})
        metric = series.get("metric", {})
        name = _series_name(metric)
        label = _restart_label(metric, name)

        for started_at in starts:
            if started_at >= first_ts:  # a start observed within the window is a restart
                out.append(
                    {"t": started_at, "label": label, "kind": "restart", "series": name}
                )

    out.sort(key=lambda event: event["t"])
    return out


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
