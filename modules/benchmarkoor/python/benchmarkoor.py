"""Thin benchmarkoor wrappers over server operations.

Benchmarkoor benchmarks Ethereum execution clients (geth, reth, nethermind,
besu, erigon, ...) by replaying standardized test fixtures and measuring gas
throughput (MGas/s), per-test wall time, system resources, and per-block EL
timing internals.

Runs of the same test set share a ``suite_hash``; use it to compare clients
on identical workloads. Query functions accept PostgREST-style ``filters``
(``{"column": "operator.value"}``) with operators ``eq``, ``neq``, ``gt``,
``gte``, ``lt``, ``lte``, ``like``, ``in``, ``is``, plus ``order`` such as
``"timestamp.desc"``.

The ``datasource`` parameter can be omitted when a single benchmarkoor
datasource is configured.
"""

from __future__ import annotations

import json
import os
from typing import Any

from ethpandaops import _runtime


def _require_benchmarkoor_available() -> None:
    if not os.environ.get("ETHPANDAOPS_BENCHMARKOOR_DATASOURCES", "").strip():
        raise ValueError(
            "Benchmarkoor is not enabled or no benchmarkoor datasources are available."
        )


def _query_args(
    datasource: str | None,
    filters: dict[str, str] | None,
    order: str | None,
    select: str | None,
    limit: int,
    offset: int,
    **eq_args: str | None,
) -> dict[str, Any]:
    args: dict[str, Any] = {"limit": limit, "offset": offset}
    if datasource is not None:
        args["datasource"] = datasource
    if filters is not None:
        args["filters"] = filters
    if order is not None:
        args["order"] = order
    if select is not None:
        args["select"] = select
    for key, value in eq_args.items():
        if value is not None:
            args[key] = value
    return args


def _rows(body: Any) -> list[dict[str, Any]]:
    if isinstance(body, dict):
        return body.get("data") or []
    return body or []


def list_datasources() -> list[dict[str, Any]]:
    """List available benchmarkoor datasources.

    Each entry has keys: name, description, url, type.
    """
    _require_benchmarkoor_available()
    data = _runtime.invoke_data("benchmarkoor.list_datasources")
    return data.get("datasources", [])


def list_runs(
    datasource: str | None = None,
    client: str | None = None,
    status: str | None = None,
    suite_hash: str | None = None,
    filters: dict[str, str] | None = None,
    order: str | None = None,
    select: str | None = None,
    limit: int = 100,
    offset: int = 0,
) -> list[dict[str, Any]]:
    """Query indexed benchmark runs.

    ``client``/``status``/``suite_hash`` are equality shorthands; use
    ``filters`` for other columns or operators (e.g.
    ``{"tests_failed": "gt.0", "timestamp": "gte.1718000000"}``).
    Statuses: running, completed, failed, container_died, cancelled, timeout.
    """
    _require_benchmarkoor_available()
    args = _query_args(
        datasource, filters, order, select, limit, offset,
        client=client, status=status, suite_hash=suite_hash,
    )
    return _rows(_runtime.invoke_json("benchmarkoor.list_runs", args))


def get_run(run_id: str, datasource: str | None = None) -> dict[str, Any]:
    """Get one indexed run by run_id, including per-step stats (steps_json)."""
    _require_benchmarkoor_available()
    args: dict[str, Any] = {"run_id": run_id}
    if datasource is not None:
        args["datasource"] = datasource
    return _runtime.invoke_json("benchmarkoor.get_run", args)


def list_suites(
    datasource: str | None = None,
    filters: dict[str, str] | None = None,
    order: str | None = None,
    limit: int = 100,
    offset: int = 0,
) -> list[dict[str, Any]]:
    """List indexed test suites (suite_hash, name, tests_total)."""
    _require_benchmarkoor_available()
    args = _query_args(datasource, filters, order, None, limit, offset)
    return _rows(_runtime.invoke_json("benchmarkoor.list_suites", args))


def get_suite_stats(
    suite_hash: str,
    datasource: str | None = None,
    max_runs_per_client: int | None = None,
) -> dict[str, Any]:
    """Per-test duration/gas history for a suite, keyed by test name.

    Each test maps to ``{"durations": [...]}`` covering recent runs per
    client — the cross-client comparison view for one workload.
    """
    _require_benchmarkoor_available()
    args: dict[str, Any] = {"suite_hash": suite_hash}
    if datasource is not None:
        args["datasource"] = datasource
    if max_runs_per_client is not None:
        args["max_runs_per_client"] = max_runs_per_client
    return _runtime.invoke_json("benchmarkoor.get_suite_stats", args)


def query_test_stats(
    datasource: str | None = None,
    run_id: str | None = None,
    client: str | None = None,
    test_name: str | None = None,
    suite_hash: str | None = None,
    filters: dict[str, str] | None = None,
    order: str | None = None,
    select: str | None = None,
    limit: int = 100,
    offset: int = 0,
) -> list[dict[str, Any]]:
    """Query per-test stats rows.

    Columns include total/setup/test ``*_gas_used``, ``*_time_ns``,
    ``*_mgas_s``, and ``test_resource_*`` (CPU usec, memory bytes, disk
    bytes/IOPS).
    """
    _require_benchmarkoor_available()
    args = _query_args(
        datasource, filters, order, select, limit, offset,
        run_id=run_id, client=client, test_name=test_name, suite_hash=suite_hash,
    )
    return _rows(_runtime.invoke_json("benchmarkoor.query_test_stats", args))


def query_block_logs(
    datasource: str | None = None,
    run_id: str | None = None,
    client: str | None = None,
    test_name: str | None = None,
    filters: dict[str, str] | None = None,
    order: str | None = None,
    select: str | None = None,
    limit: int = 100,
    offset: int = 0,
) -> list[dict[str, Any]]:
    """Query per-block EL timing rows.

    Columns include ``timing_execution_ms``, ``timing_state_read_ms``,
    ``timing_state_hash_ms``, ``timing_commit_ms``, ``timing_total_ms``,
    ``throughput_mgas_per_sec``, plus ``state_read_*``/``state_write_*``
    and ``cache_*`` counters.
    """
    _require_benchmarkoor_available()
    args = _query_args(
        datasource, filters, order, select, limit, offset,
        run_id=run_id, client=client, test_name=test_name,
    )
    return _rows(_runtime.invoke_json("benchmarkoor.query_block_logs", args))


def list_live_runs(datasource: str | None = None) -> list[dict[str, Any]]:
    """List currently-running benchmark runs with live progress."""
    _require_benchmarkoor_available()
    args: dict[str, Any] = {}
    if datasource is not None:
        args["datasource"] = datasource
    body = _runtime.invoke_json("benchmarkoor.list_live_runs", args)
    return body or []


def get_index(datasource: str | None = None) -> dict[str, Any]:
    """Full run index: every run with instance metadata and step summaries."""
    _require_benchmarkoor_available()
    args: dict[str, Any] = {}
    if datasource is not None:
        args["datasource"] = datasource
    return _runtime.invoke_json("benchmarkoor.get_index", args)


def get_file(path: str, datasource: str | None = None) -> Any:
    """Fetch a raw stored result file.

    ``path`` is relative to a discovery path, e.g.
    ``"<discovery_path>/runs/<run_id>/result.json"`` (each run's
    ``discovery_path`` comes back on its index entry). JSON responses are
    parsed to a dict/list — including S3-backed instances, which return
    ``{"url": "<presigned url>"}`` instead of streaming the file. Other
    content comes back as raw bytes.
    """
    _require_benchmarkoor_available()
    args: dict[str, Any] = {"path": path}
    if datasource is not None:
        args["datasource"] = datasource
    body, content_type = _runtime._invoke_bytes("benchmarkoor.get_file", args)
    if "application/json" in content_type:
        return json.loads(body)
    return body


def link_run(run_id: str, datasource: str | None = None) -> str:
    """Deep link to a run in the benchmarkoor web UI."""
    _require_benchmarkoor_available()
    args: dict[str, Any] = {"run_id": run_id}
    if datasource is not None:
        args["datasource"] = datasource
    data = _runtime.invoke_data("benchmarkoor.link_run", args)
    return data.get("url", "")


def link_suite(suite_hash: str, datasource: str | None = None) -> str:
    """Deep link to a suite in the benchmarkoor web UI."""
    _require_benchmarkoor_available()
    args: dict[str, Any] = {"suite_hash": suite_hash}
    if datasource is not None:
        args["datasource"] = datasource
    data = _runtime.invoke_data("benchmarkoor.link_suite", args)
    return data.get("url", "")
