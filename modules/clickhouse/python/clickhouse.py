"""Thin ClickHouse wrappers over the server operation API."""

from __future__ import annotations

from dataclasses import dataclass
import re
from typing import Any, Mapping, Sequence

import pandas as pd

from ethpandaops import _runtime


_ANSI_ESCAPE_RE_SQL = r"\x1b\[[0-9;?]*[A-Za-z]"
_ERROR_LEVEL_RE = (
    r"(^|[][ |])(CRIT|ERRO|ERROR|FATAL|PANIC)($|[][ |:])"
    r"|^(ERR|FAT)\b|\blevel=(crit|error|fatal|panic)\b"
)
_WARN_OR_ERROR_LEVEL_RE = (
    r"(^|[][ |])(CRIT|ERRO|ERROR|FATAL|PANIC|WARN|WRN)($|[][ |:])"
    r"|^(ERR|FAT|WRN)\b|\blevel=(crit|error|fatal|panic|warn|warning)\b"
)
_DEBUG_TRACE_LEVEL_RE = (
    r"(^|[][ |])(DEBUG|DBG|TRACE|TRC)($|[][ |:])"
    r"|\blevel=(debug|trace)\b"
)
_DURATION_RE = re.compile(r"^\s*(\d+)\s*([smhdw])\s*$", re.IGNORECASE)
_DURATION_UNITS = {
    "s": "SECOND",
    "m": "MINUTE",
    "h": "HOUR",
    "d": "DAY",
    "w": "WEEK",
}


@dataclass(frozen=True)
class _LogSource:
    name: str
    description: str
    datasource: str
    table: str
    timestamp: str
    body: str
    severity_number: str
    severity_text: str
    level: str
    fields: Mapping[str, str]
    compact_fields: tuple[str, ...]
    required_scope_fields: tuple[str, ...]


_LOG_SOURCES: dict[str, _LogSource] = {
    "hosted_devnet": _LogSource(
        name="hosted_devnet",
        description=(
            "Hosted multi-VM devnet and testnet container logs in "
            "clickhouse-raw.external.otel_logs."
        ),
        datasource="clickhouse-raw",
        table="external.otel_logs",
        timestamp="Timestamp",
        body="Body",
        severity_number="SeverityNumber",
        severity_text="SeverityText",
        level="LogAttributes['level']",
        fields={
            "timestamp": "Timestamp",
            "body": "Body",
            "severity_number": "SeverityNumber",
            "severity_text": "SeverityText",
            "level": "LogAttributes['level']",
            "network": "ResourceAttributes['network']",
            "host": "ResourceAttributes['host.name']",
            "service": "ServiceName",
            "container": "LogAttributes['log.file.name']",
            "container_id": "LogAttributes['container_id']",
            "ingress_user": "ResourceAttributes['ingress_user']",
            "environment": "ResourceAttributes['deployment.environment']",
        },
        compact_fields=("network", "host", "service", "container"),
        required_scope_fields=("network",),
    ),
    "local_kurtosis": _LogSource(
        name="local_kurtosis",
        description=(
            "Local Kurtosis devnet OTel logs in "
            "local-kurtosis.otel.otel_logs."
        ),
        datasource="local-kurtosis",
        table="otel.otel_logs",
        timestamp="Timestamp",
        body="Body",
        severity_number="SeverityNumber",
        severity_text="SeverityText",
        level="LogAttributes['level']",
        fields={
            "timestamp": "Timestamp",
            "body": "Body",
            "severity_number": "SeverityNumber",
            "severity_text": "SeverityText",
            "level": "LogAttributes['level']",
            "enclave": "EnclaveName",
            "service": "ServiceName",
            "host": "ResourceAttributes['host.name']",
            "container": "LogAttributes['log.file.name']",
            "container_id": "LogAttributes['container_id']",
        },
        compact_fields=("enclave", "service", "host", "container"),
        required_scope_fields=("enclave",),
    ),
}


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
    return _runtime.invoke_tsv_dataframe(
        "clickhouse.query",
        {
            "datasource": datasource,
            "sql": sql,
            "parameters": parameters,
        },
    )


def query_raw(
    datasource: str,
    sql: str,
    parameters: dict[str, Any] | None = None,
) -> tuple[list[tuple], list[str]]:
    """Execute a SQL query and return raw rows plus column names."""
    return _runtime.invoke_tsv_rows(
        "clickhouse.query_raw",
        {
            "datasource": datasource,
            "sql": sql,
            "parameters": parameters,
        },
    )


def log_sources() -> list[dict[str, Any]]:
    """List built-in ClickHouse log source presets.

    These presets encode the datasource, table, timestamp, identity fields, and
    scope rules needed for token-efficient devnet log investigation.
    """
    return [
        {
            "name": source.name,
            "description": source.description,
            "datasource": source.datasource,
            "table": source.table,
            "timestamp": source.timestamp,
            "body": source.body,
            "fields": sorted(source.fields.keys()),
            "compact_fields": list(source.compact_fields),
            "required_scope_fields": list(source.required_scope_fields),
        }
        for source in _LOG_SOURCES.values()
    ]


def log_coverage(
    source: str,
    filters: Mapping[str, Any],
    *,
    like_filters: Mapping[str, str] | None = None,
    exclude_filters: Mapping[str, Any] | None = None,
    since: str = "1h",
    until: str | None = None,
    include_sql: bool = False,
) -> dict[str, Any]:
    """Measure severity field coverage for a scoped log slice.

    Use this before broad log triage to see whether structured severity fields
    are populated or whether the bounded body fallback is needed.
    """
    log_source = _get_log_source(source)
    exact_filters = _normalize_filters("filters", filters, log_source)
    _require_scope(log_source, exact_filters, "log_coverage")

    params: dict[str, Any] = {}
    clauses = _build_where_clauses(
        log_source,
        params,
        filters=exact_filters,
        like_filters=_normalize_filters("like_filters", like_filters, log_source),
        exclude_filters=_normalize_filters(
            "exclude_filters", exclude_filters, log_source
        ),
        since=since,
        until=until,
    )

    sql = f"""
SELECT
  count() AS lines,
  countIf({log_source.severity_number} != 0) AS severity_number_lines,
  countIf({log_source.severity_text} != '') AS severity_text_lines,
  countIf({log_source.level} != '') AS log_attr_level_lines,
  min({log_source.timestamp}) AS first_seen,
  max({log_source.timestamp}) AS last_seen
FROM {log_source.table}
{_where_sql(clauses)}
""".strip()

    rows, columns = query_raw(log_source.datasource, sql, params)
    data = _first_row_dict(rows, columns)
    lines = _to_int(data.get("lines"))

    result: dict[str, Any] = {
        "source": log_source.name,
        "datasource": log_source.datasource,
        "table": log_source.table,
        "filters": _public_filter_summary(exact_filters, like_filters, exclude_filters),
        "lines": lines,
        "severity_number_lines": _to_int(data.get("severity_number_lines")),
        "severity_text_lines": _to_int(data.get("severity_text_lines")),
        "log_attr_level_lines": _to_int(data.get("log_attr_level_lines")),
        "first_seen": data.get("first_seen", ""),
        "last_seen": data.get("last_seen", ""),
    }
    if lines:
        result["severity_number_coverage"] = result["severity_number_lines"] / lines
        result["severity_text_coverage"] = result["severity_text_lines"] / lines
        result["log_attr_level_coverage"] = result["log_attr_level_lines"] / lines

    _maybe_attach_query(result, include_sql, log_source.datasource, sql, params)
    return result


def log_values(
    source: str,
    field: str,
    filters: Mapping[str, Any] | None = None,
    *,
    like_filters: Mapping[str, str] | None = None,
    exclude_filters: Mapping[str, Any] | None = None,
    since: str = "1h",
    until: str | None = None,
    limit: int = 20,
) -> pd.DataFrame:
    """Return top values for a log field within a bounded slice."""
    log_source = _get_log_source(source)
    field_expr = _field_expr(log_source, field)
    exact_filters = _normalize_filters("filters", filters, log_source)

    # Discovering the required scope field itself is allowed without a scope
    # filter. Drilling into other fields must be scoped to avoid wide scans.
    if field not in log_source.required_scope_fields:
        _require_scope(log_source, exact_filters, "log_values")

    params: dict[str, Any] = {}
    clauses = _build_where_clauses(
        log_source,
        params,
        filters=exact_filters,
        like_filters=_normalize_filters("like_filters", like_filters, log_source),
        exclude_filters=_normalize_filters(
            "exclude_filters", exclude_filters, log_source
        ),
        since=since,
        until=until,
    )
    limit_ref = _add_param(params, "limit", _validate_int("limit", limit, 1, 500), "UInt64")

    sql = f"""
SELECT
  {field_expr} AS value,
  count() AS lines,
  min({log_source.timestamp}) AS first_seen,
  max({log_source.timestamp}) AS last_seen
FROM {log_source.table}
{_where_sql(clauses)}
GROUP BY value
ORDER BY lines DESC, value ASC
LIMIT {limit_ref}
""".strip()

    return query(log_source.datasource, sql, params)


def log_samples(
    source: str,
    field: str,
    filters: Mapping[str, Any],
    *,
    like_filters: Mapping[str, str] | None = None,
    exclude_filters: Mapping[str, Any] | None = None,
    since: str = "1h",
    until: str | None = None,
    limit: int = 20,
    body_chars: int = 160,
    include_sql: bool = False,
) -> dict[str, Any]:
    """Return top field values with counts and one compact sample log body."""
    log_source = _get_log_source(source)
    field_expr = _field_expr(log_source, field)
    exact_filters = _normalize_filters("filters", filters, log_source)
    _require_scope(log_source, exact_filters, "log_samples")

    params: dict[str, Any] = {}
    clauses = _build_where_clauses(
        log_source,
        params,
        filters=exact_filters,
        like_filters=_normalize_filters("like_filters", like_filters, log_source),
        exclude_filters=_normalize_filters(
            "exclude_filters", exclude_filters, log_source
        ),
        since=since,
        until=until,
    )
    limit_value = _validate_int("limit", limit, 1, 200)
    body_chars_value = _validate_int("body_chars", body_chars, 40, 1000)
    limit_ref = _add_param(params, "limit", limit_value, "UInt64")
    body_chars_ref = _add_param(params, "body_chars", body_chars_value, "UInt64")

    sql = f"""
WITH {_clean_body_expr(log_source)} AS clean
SELECT
  {field_expr} AS value,
  count() AS lines,
  min({log_source.timestamp}) AS first_seen,
  max({log_source.timestamp}) AS last_seen,
  any(substring(clean, 1, {body_chars_ref})) AS sample
FROM {log_source.table}
{_where_sql(clauses)}
GROUP BY value
ORDER BY lines DESC, value ASC
LIMIT {limit_ref}
""".strip()

    rows, columns = query_raw(log_source.datasource, sql, params)
    row_dicts = [dict(zip(columns, row)) for row in rows]
    for row in row_dicts:
        row["lines"] = _to_int(row.get("lines"))

    result: dict[str, Any] = {
        "source": log_source.name,
        "datasource": log_source.datasource,
        "table": log_source.table,
        "field": field,
        "filters": _public_filter_summary(
            exact_filters, like_filters, exclude_filters
        ),
        "rows_returned": len(row_dicts),
        "limit": limit_value,
        "rows_limited": len(row_dicts) >= limit_value,
        "body_chars": body_chars_value,
        "rows": row_dicts,
    }
    _maybe_attach_query(result, include_sql, log_source.datasource, sql, params)
    return result


def log_errors(
    source: str,
    filters: Mapping[str, Any],
    *,
    like_filters: Mapping[str, str] | None = None,
    exclude_filters: Mapping[str, Any] | None = None,
    since: str = "1h",
    until: str | None = None,
    min_severity: str = "error",
    limit: int = 50,
    body_chars: int = 240,
    include_sql: bool = False,
) -> dict[str, Any]:
    """Fetch compact warning/error-class log rows for a scoped log slice.

    The generated SQL prefers structured OTel severity and falls back to a
    bounded, ANSI-stripped body regex for raw Docker log lines that have empty
    severity fields. Set min_severity="warn" to include WARN/WRN rows.
    """
    log_source = _get_log_source(source)
    exact_filters = _normalize_filters("filters", filters, log_source)
    _require_scope(log_source, exact_filters, "log_errors")

    params: dict[str, Any] = {}
    clauses = _build_where_clauses(
        log_source,
        params,
        filters=exact_filters,
        like_filters=_normalize_filters("like_filters", like_filters, log_source),
        exclude_filters=_normalize_filters(
            "exclude_filters", exclude_filters, log_source
        ),
        since=since,
        until=until,
    )
    clauses.append(_severity_clause(log_source, params, min_severity))

    limit_value = _validate_int("limit", limit, 1, 500)
    body_chars_value = _validate_int("body_chars", body_chars, 40, 4000)
    limit_ref = _add_param(params, "limit", limit_value, "UInt64")
    body_chars_ref = _add_param(params, "body_chars", body_chars_value, "UInt64")

    sql = f"""
WITH {_clean_body_expr(log_source)} AS clean
SELECT
{_compact_select_list(log_source, body_chars_ref)}
FROM {log_source.table}
{_where_sql(clauses)}
ORDER BY {log_source.timestamp} DESC
LIMIT {limit_ref}
""".strip()

    rows, columns = query_raw(log_source.datasource, sql, params)
    result = _compact_result(
        log_source=log_source,
        sql=sql,
        params=params,
        rows=rows,
        columns=columns,
        limit=limit_value,
        body_chars=body_chars_value,
        include_sql=include_sql,
    )
    result["kind"] = f"{_normalize_min_severity(min_severity)}_logs"
    result["filters"] = _public_filter_summary(
        exact_filters, like_filters, exclude_filters
    )
    return result


def log_context(
    source: str,
    filters: Mapping[str, Any],
    timestamp: str,
    *,
    like_filters: Mapping[str, str] | None = None,
    exclude_filters: Mapping[str, Any] | None = None,
    before: int = 20,
    after: int = 20,
    window: str | None = "1h",
    body_chars: int = 240,
    include_sql: bool = False,
) -> dict[str, Any]:
    """Fetch compact log context before and after a timestamp."""
    if not timestamp:
        raise ValueError("timestamp is required")

    log_source = _get_log_source(source)
    exact_filters = _normalize_filters("filters", filters, log_source)
    _require_scope(log_source, exact_filters, "log_context")

    params: dict[str, Any] = {}
    filter_clauses = _build_filter_clauses(
        log_source,
        params,
        filters=exact_filters,
        like_filters=_normalize_filters("like_filters", like_filters, log_source),
        exclude_filters=_normalize_filters(
            "exclude_filters", exclude_filters, log_source
        ),
    )
    where_scope = _indent(_where_sql(filter_clauses), 4)
    center_ref = _add_param(params, "center", timestamp, "String")
    before_ref = _add_param(
        params, "before", _validate_int("before", before, 0, 200), "UInt64"
    )
    after_ref = _add_param(
        params, "after", _validate_int("after", after, 0, 200), "UInt64"
    )
    body_chars_value = _validate_int("body_chars", body_chars, 40, 4000)
    body_chars_ref = _add_param(params, "body_chars", body_chars_value, "UInt64")
    center_expr = f"parseDateTime64BestEffort({center_ref}, 9)"
    before_window_clause = ""
    after_window_clause = ""
    if window is not None:
        interval = _duration_interval("window", window)
        before_window_clause = f"\n      AND {log_source.timestamp} >= center - {interval}"
        after_window_clause = f"\n      AND {log_source.timestamp} <= center + {interval}"
    select_list = _compact_select_list(
        log_source,
        body_chars_ref,
        clean_expr=_clean_body_expr(log_source),
    )

    sql = f"""
WITH {center_expr} AS center
SELECT *
FROM
(
  SELECT *
  FROM
  (
    SELECT
      'before' AS context,
{_indent(select_list, 4)}
    FROM {log_source.table}
{where_scope}
      AND {log_source.timestamp} < center
{before_window_clause}
    ORDER BY {log_source.timestamp} DESC
    LIMIT {before_ref}
  )

  UNION ALL

  SELECT *
  FROM
  (
    SELECT
      'at' AS context,
{_indent(select_list, 4)}
    FROM {log_source.table}
{where_scope}
      AND {log_source.timestamp} = center
    ORDER BY {log_source.timestamp} ASC
    LIMIT 1
  )

  UNION ALL

  SELECT *
  FROM
  (
    SELECT
      'after' AS context,
{_indent(select_list, 4)}
    FROM {log_source.table}
{where_scope}
      AND {log_source.timestamp} > center
{after_window_clause}
    ORDER BY {log_source.timestamp} ASC
    LIMIT {after_ref}
  )
)
ORDER BY ts ASC
""".strip()

    rows, columns = query_raw(log_source.datasource, sql, params)
    result = _compact_result(
        log_source=log_source,
        sql=sql,
        params=params,
        rows=rows,
        columns=columns,
        limit=_validate_int("before", before, 0, 200)
        + _validate_int("after", after, 0, 200)
        + 1,
        body_chars=body_chars_value,
        include_sql=include_sql,
    )
    result["kind"] = "log_context"
    result["center_timestamp"] = timestamp
    result["window"] = window
    result["filters"] = _public_filter_summary(
        exact_filters, like_filters, exclude_filters
    )
    return result


def _get_log_source(source: str) -> _LogSource:
    if source not in _LOG_SOURCES:
        names = ", ".join(sorted(_LOG_SOURCES))
        raise ValueError(f"unknown log source {source!r}; expected one of: {names}")
    return _LOG_SOURCES[source]


def _field_expr(source: _LogSource, field: str) -> str:
    if field not in source.fields:
        names = ", ".join(sorted(source.fields))
        raise ValueError(
            f"unknown field {field!r} for log source {source.name!r}; "
            f"expected one of: {names}"
        )
    return source.fields[field]


def _field_param_type(field: str) -> str:
    if field == "severity_number":
        return "UInt8"
    return "String"


def _normalize_filters(
    name: str,
    filters: Mapping[str, Any] | None,
    source: _LogSource,
) -> dict[str, Any]:
    if filters is None:
        return {}
    if not isinstance(filters, Mapping):
        raise TypeError(f"{name} must be a mapping of field name to value")

    normalized: dict[str, Any] = {}
    for field, value in filters.items():
        if not isinstance(field, str):
            raise TypeError(f"{name} keys must be field names")
        _field_expr(source, field)
        if value is None:
            raise ValueError(f"{name}[{field!r}] cannot be None")
        normalized[field] = value
    return normalized


def _require_scope(
    source: _LogSource,
    filters: Mapping[str, Any],
    operation: str,
) -> None:
    for field in source.required_scope_fields:
        if field in filters:
            return

    required = " or ".join(repr(field) for field in source.required_scope_fields)
    raise ValueError(
        f"{operation} requires an exact scope filter for {required} on "
        f"log source {source.name!r}"
    )


def _build_where_clauses(
    source: _LogSource,
    params: dict[str, Any],
    *,
    filters: Mapping[str, Any],
    like_filters: Mapping[str, str],
    exclude_filters: Mapping[str, Any],
    since: str | None,
    until: str | None,
) -> list[str]:
    clauses = _build_filter_clauses(
        source,
        params,
        filters=filters,
        like_filters=like_filters,
        exclude_filters=exclude_filters,
    )
    clauses.extend(_time_clauses(source, params, since=since, until=until))
    return clauses


def _build_filter_clauses(
    source: _LogSource,
    params: dict[str, Any],
    *,
    filters: Mapping[str, Any],
    like_filters: Mapping[str, str],
    exclude_filters: Mapping[str, Any],
) -> list[str]:
    clauses: list[str] = []

    for field, value in filters.items():
        expr = _field_expr(source, field)
        clauses.append(
            _comparison_clause(params, field, expr, value, _field_param_type(field), "=")
        )

    for field, value in like_filters.items():
        if _is_sequence(value):
            raise ValueError(f"like_filters[{field!r}] must be a single LIKE pattern")
        expr = _field_expr(source, field)
        value_ref = _add_param(params, f"{field}_like", value, "String")
        clauses.append(f"{expr} LIKE {value_ref}")

    for field, value in exclude_filters.items():
        expr = _field_expr(source, field)
        clauses.append(
            _comparison_clause(params, field, expr, value, _field_param_type(field), "!=")
        )

    return clauses


def _comparison_clause(
    params: dict[str, Any],
    field: str,
    expr: str,
    value: Any,
    param_type: str,
    operator: str,
) -> str:
    if _is_sequence(value):
        values = list(value)
        if not values:
            raise ValueError(f"filter {field!r} cannot use an empty sequence")
        refs = [
            _add_param(params, field, item, param_type)
            for item in values
        ]
        sql_operator = "IN" if operator == "=" else "NOT IN"
        return f"{expr} {sql_operator} ({', '.join(refs)})"

    value_ref = _add_param(params, field, value, param_type)
    return f"{expr} {operator} {value_ref}"


def _time_clauses(
    source: _LogSource,
    params: dict[str, Any],
    *,
    since: str | None,
    until: str | None,
) -> list[str]:
    clauses: list[str] = []
    if since:
        clauses.append(f"{source.timestamp} >= {_time_expr(params, 'since', since)}")
    if until:
        clauses.append(f"{source.timestamp} < {_time_expr(params, 'until', until)}")
    return clauses


def _time_expr(params: dict[str, Any], name: str, value: str) -> str:
    if not isinstance(value, str):
        raise TypeError(f"{name} must be a relative duration string or timestamp string")

    normalized = value.strip()
    if not normalized:
        raise ValueError(f"{name} cannot be empty")
    if normalized.lower() == "now":
        return "now()"

    match = _DURATION_RE.match(normalized)
    if match:
        amount = int(match.group(1))
        if amount <= 0:
            raise ValueError(f"{name} duration must be positive")
        unit = _DURATION_UNITS[match.group(2).lower()]
        return f"now() - INTERVAL {amount} {unit}"

    value_ref = _add_param(params, name, normalized, "String")
    return f"parseDateTime64BestEffort({value_ref}, 9)"


def _duration_interval(name: str, value: str) -> str:
    if not isinstance(value, str):
        raise TypeError(f"{name} must be a relative duration string")

    match = _DURATION_RE.match(value.strip())
    if not match:
        raise ValueError(f"{name} must be a relative duration like '15m' or '1h'")

    amount = int(match.group(1))
    if amount <= 0:
        raise ValueError(f"{name} duration must be positive")

    unit = _DURATION_UNITS[match.group(2).lower()]
    return f"INTERVAL {amount} {unit}"


def _severity_clause(
    source: _LogSource,
    params: dict[str, Any],
    min_severity: str,
) -> str:
    normalized = _normalize_min_severity(min_severity)
    if normalized == "warn":
        severity_number = 13
        severity_texts = (
            "'WARN', 'WARNING', 'WRN', 'CRIT', 'CRITICAL', "
            "'ERRO', 'ERROR', 'FATAL', 'PANIC'"
        )
        attr_levels = (
            "'warn', 'warning', 'wrn', 'crit', 'critical', "
            "'erro', 'error', 'fatal', 'panic'"
        )
        level_re = _WARN_OR_ERROR_LEVEL_RE
    else:
        severity_number = 17
        severity_texts = "'CRIT', 'CRITICAL', 'ERRO', 'ERROR', 'FATAL', 'PANIC'"
        attr_levels = "'crit', 'critical', 'erro', 'error', 'fatal', 'panic'"
        level_re = _ERROR_LEVEL_RE

    error_re = _add_param(params, "level_re", level_re, "String")
    debug_trace_re = _add_param(
        params, "debug_trace_level_re", _DEBUG_TRACE_LEVEL_RE, "String"
    )

    return f"""(
  {source.severity_number} >= {severity_number}
  OR upper({source.severity_text}) IN ({severity_texts})
  OR lower({source.level}) IN ({attr_levels})
  OR (
    match(clean, {error_re})
    AND NOT match(clean, {debug_trace_re})
  )
)"""


def _normalize_min_severity(value: str) -> str:
    if value not in ("error", "warn"):
        raise ValueError("min_severity must be 'error' or 'warn'")
    return value


def _clean_body_expr(source: _LogSource) -> str:
    return f"replaceRegexpAll({source.body}, '{_ANSI_ESCAPE_RE_SQL}', '')"


def _compact_select_list(
    source: _LogSource,
    body_chars_ref: str,
    *,
    clean_expr: str = "clean",
) -> str:
    fields = [
        f"  {source.timestamp} AS ts",
    ]
    for field in source.compact_fields:
        fields.append(f"  {_field_expr(source, field)} AS {field}")

    fields.extend(
        [
            (
                "  multiIf("
                f"{source.severity_text} != '', {source.severity_text}, "
                f"{source.level} != '', {source.level}, "
                f"{source.severity_number} != 0, toString({source.severity_number}), "
                "''"
                ") AS severity"
            ),
            f"  {source.severity_number} AS severity_number",
            f"  substring({clean_expr}, 1, {body_chars_ref}) AS body",
            f"  length({clean_expr}) > {body_chars_ref} AS body_truncated",
        ]
    )
    return ",\n".join(fields)


def _compact_result(
    *,
    log_source: _LogSource,
    sql: str,
    params: Mapping[str, Any],
    rows: Sequence[Sequence[Any]],
    columns: Sequence[str],
    limit: int,
    body_chars: int,
    include_sql: bool,
) -> dict[str, Any]:
    row_dicts = [_compact_row(dict(zip(columns, row))) for row in rows]
    result: dict[str, Any] = {
        "source": log_source.name,
        "datasource": log_source.datasource,
        "table": log_source.table,
        "rows_returned": len(row_dicts),
        "limit": limit,
        "rows_limited": len(row_dicts) >= limit,
        "body_chars": body_chars,
        "rows": row_dicts,
    }
    _maybe_attach_query(result, include_sql, log_source.datasource, sql, params)
    return result


def _compact_row(row: dict[str, Any]) -> dict[str, Any]:
    compact = dict(row)
    if "body_truncated" in compact:
        compact["body_truncated"] = _to_bool(compact["body_truncated"])
    if "severity_number" in compact:
        compact["severity_number"] = _to_int(compact["severity_number"])
    return compact


def _maybe_attach_query(
    result: dict[str, Any],
    include_sql: bool,
    datasource: str,
    sql: str,
    params: Mapping[str, Any],
) -> None:
    if include_sql:
        result["query"] = {
            "datasource": datasource,
            "sql": sql,
            "parameters": dict(params),
        }
    else:
        result["query_omitted"] = "pass include_sql=True to include reproducible SQL"


def _public_filter_summary(
    filters: Mapping[str, Any],
    like_filters: Mapping[str, Any] | None,
    exclude_filters: Mapping[str, Any] | None,
) -> dict[str, Any]:
    result: dict[str, Any] = {"filters": dict(filters)}
    if like_filters:
        result["like_filters"] = dict(like_filters)
    if exclude_filters:
        result["exclude_filters"] = dict(exclude_filters)
    return result


def _where_sql(clauses: Sequence[str]) -> str:
    if not clauses:
        return ""
    return "WHERE " + "\n  AND ".join(clauses)


def _add_param(
    params: dict[str, Any],
    name: str,
    value: Any,
    param_type: str,
) -> str:
    safe_name = re.sub(r"[^a-zA-Z0-9_]", "_", name).strip("_") or "param"
    candidate = safe_name
    suffix = 2
    while candidate in params:
        candidate = f"{safe_name}_{suffix}"
        suffix += 1

    params[candidate] = value
    return f"{{{candidate}:{param_type}}}"


def _validate_int(name: str, value: int, minimum: int, maximum: int) -> int:
    if not isinstance(value, int):
        raise TypeError(f"{name} must be an integer")
    if value < minimum or value > maximum:
        raise ValueError(f"{name} must be between {minimum} and {maximum}")
    return value


def _first_row_dict(
    rows: Sequence[Sequence[Any]],
    columns: Sequence[str],
) -> dict[str, Any]:
    if not rows:
        return {}
    return dict(zip(columns, rows[0]))


def _to_int(value: Any) -> int:
    if value in (None, ""):
        return 0
    return int(value)


def _to_bool(value: Any) -> bool:
    if isinstance(value, bool):
        return value
    if value in (1, "1", "true", "True", "TRUE"):
        return True
    return False


def _is_sequence(value: Any) -> bool:
    return isinstance(value, Sequence) and not isinstance(value, (str, bytes, bytearray))


def _indent(value: str, spaces: int) -> str:
    prefix = " " * spaces
    return "\n".join(prefix + line for line in value.splitlines())
