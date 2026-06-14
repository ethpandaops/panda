# Panda CLI Gotchas

Common pitfalls when using the `panda` CLI, especially from scripts or agents.

## 1. Module Imports

The sandbox uses `ethpandaops` as the package name, not shorthand helpers.

```python
# Wrong
ch("xatu", "SELECT ...")
clickhouse.query("xatu", "SELECT ...")

# Right
from ethpandaops import clickhouse
clickhouse.query("xatu", "SELECT ...")
```

Run `panda docs <module>` to see the correct import and function signatures.

## 2. ClickHouse Column Names

Column names vary between tables. Don't assume — verify with `system.columns` or `panda schema`.

```python
# Wrong — this column doesn't exist
SELECT meta_execution_implementation FROM beacon_api_eth_v1_events_head

# Right — check what's available
SELECT name FROM system.columns
WHERE table = 'beacon_api_eth_v1_events_head' AND name LIKE 'meta_%'
```

The actual column is `meta_client_implementation`, not `meta_execution_implementation`.

## 3. Session Limits

The sandbox enforces a max of **10 concurrent sessions**. Each `panda execute` call creates a new session by default.

```bash
# Check current sessions
panda session list

# Reuse an existing session
panda execute --session <id> --code '...'

# Clean up
panda session destroy <id>
```

If you hit `maximum sessions limit reached (10/10)`, destroy unused sessions first.

## 4. Python f-string Bracket Syntax

The sandbox runs Python 3.11, which does **not** support nested quotes inside f-string brackets.

```python
# Wrong — SyntaxError in Python 3.11
print(f"Latest slot: {df["slot"].max()}")

# Right — assign to a variable first
latest = df["slot"].max()
print(f"Latest slot: {latest}")
```

## 5. Shell Quoting for SQL Strings

When passing `--code` in single quotes, SQL string literals need careful escaping.

```bash
# Wrong — breaks the shell quoting
panda execute --code '... WHERE name = 'foo' ...'

# Right — use quote-escape-quote pattern
panda execute --code '... WHERE name = '"'"'foo'"'"' ...'
```

## 6. ethnode API

There is no `list_networks()` on the ethnode module. You need to know the network name and node name upfront.

Node naming convention: `{cl_client}-{el_client}-{index}` (e.g., `lighthouse-reth-super-1`).

```bash
# Check the actual API
panda docs ethnode
```

## 7. ClickHouse Cluster Syntax

Xatu data is split across clusters with **different query syntax**:

| Cluster | Table syntax | Network filter |
|---------|-------------|----------------|
| `xatu` | `FROM table_name` | `WHERE meta_network_name = '...'` |
| `xatu-cbt` | `FROM network.table_name` | Database prefix is the filter |
| `xatu-experimental` | `FROM table_name` | `WHERE meta_network_name = '...'` (devnets only) |

## 8. Partition Key Filtering

Always filter by the table's partition key (usually `slot_start_date_time`) to avoid query timeouts. Unfiltered queries on large tables will be slow or fail.

```python
# Wrong — full table scan
clickhouse.query("xatu", "SELECT * FROM beacon_api_eth_v1_events_block LIMIT 10")

# Right — partition-aware
clickhouse.query("xatu", """
    SELECT * FROM beacon_api_eth_v1_events_block
    WHERE slot_start_date_time >= now() - INTERVAL 1 HOUR
    LIMIT 10
""")
```
