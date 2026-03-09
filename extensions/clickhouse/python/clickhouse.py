"""Thin ClickHouse wrappers over the proxy operation API."""

from typing import Any

import pandas as pd

from ethpandaops import _runtime


def list_datasources() -> list[dict[str, Any]]:
    """List available ClickHouse clusters."""
    response = _runtime.invoke("clickhouse.list_datasources")
    data = response.get("data", {})
    datasources = data.get("datasources", [])
    if not isinstance(datasources, list):
        raise ValueError("Invalid clickhouse.list_datasources response shape")
    return datasources


def query(
    cluster_name: str,
    sql: str,
    parameters: dict[str, Any] | None = None,
) -> pd.DataFrame:
    """Execute a SQL query against a ClickHouse cluster."""
    return _runtime.invoke_tsv_dataframe(
        "clickhouse.query",
        {
            "cluster": cluster_name,
            "sql": sql,
            "parameters": parameters,
        },
    )


def query_raw(
    cluster_name: str,
    sql: str,
    parameters: dict[str, Any] | None = None,
) -> tuple[list[tuple], list[str]]:
    """Execute a SQL query and return raw rows plus column names."""
    return _runtime.invoke_tsv_rows(
        "clickhouse.query_raw",
        {
            "cluster": cluster_name,
            "sql": sql,
            "parameters": parameters,
        },
    )
