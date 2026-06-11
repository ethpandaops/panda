"""Thin ClickHouse wrappers over the server operation API."""

from typing import Any, NoReturn

import pandas as pd

from ethpandaops import _runtime


def _query_error_hint(message: str) -> str:
    """Return concise ClickHouse guidance for common error classes.

    This is the sandbox-side sibling of the Go classifier in queryerrors.go
    (the sandbox cannot call into Go): when adding or changing an error class
    there, mirror the match patterns here.
    """
    normalized = message.lower()

    if (
        "index_not_used" in normalized
        or "force_primary_key" in normalized
        or ("primary key" in normalized and "not used" in normalized)
    ):
        return (
            "ClickHouse requires this query to use primary-key/order-key columns. "
            "Inspect the table's PARTITION/ORDER BY keys via schema discovery "
            "(clickhouse://tables/<cluster>/<database>/<table>) and "
            "add selective WHERE filters on those keys; partition filters help bound "
            "work but may not satisfy force_primary_key. Only use SETTINGS "
            "force_primary_key=0 for a bounded query you can justify."
        )

    if (
        "distributed_in_join_subquery_denied" in normalized
        or "double-distributed in/join subqueries" in normalized
    ):
        return (
            "ClickHouse denied a distributed subquery or join. Filter each side before "
            "joining, use GLOBAL/ANY JOIN when appropriate, or resolve the small side "
            "first and pass literal values into the next query."
        )

    if "syntax_error" in normalized or "syntax error" in normalized:
        return (
            "ClickHouse rejected the SQL syntax. Check the dataset's syntax rules "
            "(datasets://<dataset>) and the table schema "
            "(clickhouse://tables/<cluster>/<database>/<table>). For FINAL with an "
            "alias use FROM table AS alias FINAL. Put SETTINGS after "
            "LIMIT/OFFSET/ORDER BY clauses."
        )

    if (
        "unknown_identifier" in normalized
        or "unknown expression identifier" in normalized
        or "missing columns" in normalized
    ):
        return (
            "The SQL references a column or expression that is not available in the "
            "selected table. Inspect the table schema and adjust SELECT, WHERE, and "
            "GROUP BY clauses."
        )

    if (
        "unknown_table" in normalized
        or "unknown_database" in normalized
        or "unknown table expression identifier" in normalized
    ):
        return (
            "The SQL references a table or database that is not available in the "
            "selected datasource. List datasources (datasources://clickhouse) and inspect "
            "tables via clickhouse://tables/<cluster>/<database>."
        )

    if (
        "unknown_function" in normalized
        or ("function with name" in normalized and "does not exist" in normalized)
    ):
        return (
            "The SQL uses a ClickHouse function unavailable in this deployment. Replace "
            "it with a supported function or simplify the expression."
        )

    if "not_an_aggregate" in normalized:
        return (
            "ClickHouse requires every selected expression to be aggregated or included "
            "in GROUP BY."
        )

    if "illegal_aggregation" in normalized or (
        "aggregate function" in normalized
        and "inside another aggregate function" in normalized
    ):
        return (
            "ClickHouse does not allow nested aggregate functions. Compute the inner "
            "aggregate in a subquery or separate step before applying the outer aggregate."
        )

    if "bad_arguments" in normalized or "cannot work with" in normalized:
        return (
            "A SQL function received an incompatible argument type or shape. Inspect "
            "column types and cast deliberately where needed."
        )

    return ""


def _raise_with_query_hint(error: ValueError) -> NoReturn:
    message = str(error)
    hint = _query_error_hint(message)
    if not hint or "hint:" in message.lower():
        raise error
    raise ValueError(f"{message}\n\nhint: {hint}") from None


def list_datasources() -> list[dict[str, Any]]:
    """List available ClickHouse datasources."""
    response = _runtime.invoke("clickhouse.list_datasources")
    data = response.get("data", {})
    datasources = data.get("datasources", [])
    if not isinstance(datasources, list):
        raise ValueError("Invalid clickhouse.list_datasources response shape")
    return datasources


def query(
    datasource: str,
    sql: str,
    parameters: dict[str, Any] | None = None,
) -> pd.DataFrame:
    """Execute a SQL query against a ClickHouse datasource."""
    try:
        return _runtime.invoke_tsv_dataframe(
            "clickhouse.query",
            {
                "datasource": datasource,
                "sql": sql,
                "parameters": parameters,
            },
        )
    except ValueError as exc:
        _raise_with_query_hint(exc)


def query_raw(
    datasource: str,
    sql: str,
    parameters: dict[str, Any] | None = None,
) -> tuple[list[tuple], list[str]]:
    """Execute a SQL query and return raw rows plus column names."""
    try:
        return _runtime.invoke_tsv_rows(
            "clickhouse.query_raw",
            {
                "datasource": datasource,
                "sql": sql,
                "parameters": parameters,
            },
        )
    except ValueError as exc:
        _raise_with_query_hint(exc)
