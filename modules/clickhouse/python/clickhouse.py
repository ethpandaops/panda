"""Thin ClickHouse wrappers over the server operation API."""

from typing import Any

import pandas as pd

from ethpandaops import _runtime


def list_datasources() -> list[dict[str, Any]]:
    """List available ClickHouse datasources."""
    response = _runtime.invoke("clickhouse.list_datasources")
    data = response.get("data", {})
    datasources = data.get("datasources", [])
    if not isinstance(datasources, list):
        raise ValueError("Invalid clickhouse.list_datasources response shape")
    return datasources


def _resolve_datasource_name(
    datasource: str | None,
    cluster_name: str | None,
) -> str:
    name = datasource or cluster_name
    if not name:
        raise ValueError("datasource is required")
    return name


def query(
    datasource: str | None = None,
    sql: str = "",
    parameters: dict[str, Any] | None = None,
    *,
    cluster_name: str | None = None,
) -> pd.DataFrame:
    """Execute a SQL query against a ClickHouse datasource."""
    datasource_name = _resolve_datasource_name(datasource, cluster_name)
    return _runtime.invoke_tsv_dataframe(
        "clickhouse.query",
        {
            "datasource": datasource_name,
            "sql": sql,
            "parameters": parameters,
        },
    )


def query_raw(
    datasource: str | None = None,
    sql: str = "",
    parameters: dict[str, Any] | None = None,
    *,
    cluster_name: str | None = None,
) -> tuple[list[tuple], list[str]]:
    """Execute a SQL query and return raw rows plus column names."""
    datasource_name = _resolve_datasource_name(datasource, cluster_name)
    return _runtime.invoke_tsv_rows(
        "clickhouse.query_raw",
        {
            "datasource": datasource_name,
            "sql": sql,
            "parameters": parameters,
        },
    )
