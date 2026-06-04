---
name: query
description: Query Ethereum network data via ethpandaops CLI or MCP server. Use when analyzing blockchain data, block timing, attestations, validator performance, network health, or infrastructure metrics. Provides access to ClickHouse (blockchain data and OTel logs), Prometheus (metrics), and Dora (explorer APIs).
argument-hint: <query or question>
user-invocable: false
---

# ethpandaops Query Guide

Query Ethereum network data through the ethpandaops tools. Execute Python code in sandboxed containers with access to ClickHouse blockchain data and OTel logs, Prometheus metrics, and Dora explorer APIs.

## Workflow

1. **Discover** - Find available datasources and schemas
2. **Find patterns** - Search for query examples and runbooks
3. **Execute** - Run Python using the `ethpandaops` library

## Access Methods

This skill works with **either** the CLI (`panda` binary) or the MCP server. **Prefer the CLI** — it is always available. Only use the MCP tools (`execute_python`, `manage_session`, `search`) if they appear in your available tools list. If they do not, use the CLI equivalents below via the Bash tool.

### CLI (`panda` binary) — primary interface

```bash
# Discovery
panda datasources                          # List all datasources
panda datasources --type clickhouse        # Filter by type
panda schema                               # List all clusters and their tables
panda schema xatu                          # List tables in the xatu cluster
panda schema xatu mainnet fct_block_head   # Show a table schema (cluster database table)
panda docs                                 # List Python API modules
panda docs clickhouse                      # Show module docs

# Search
panda search examples "block arrival time"
panda search examples "attestation" --category attestations --limit 5
panda search runbooks "finality delay"
panda search runbooks "validator" --tag performance

# Execute
panda execute --code 'from ethpandaops import clickhouse; print(clickhouse.list_datasources())'
panda execute --file script.py
panda execute --code '...' --session <id>  # Reuse session
echo 'print("hello")' | panda execute

# Sessions
panda session list
panda session create
panda session destroy <session-id>
```

All commands support `--json` for structured output.

### MCP Server (when available as plugin)

| Resource | Description |
|----------|-------------|
| `datasources://list` | All configured datasources |
| `datasources://clickhouse` | ClickHouse clusters |
| `datasources://prometheus` | Prometheus instances |
| `networks://active` | Active Ethereum networks |
| `clickhouse://tables` | All clusters and their tables (keyed by database + name) |
| `clickhouse://tables/{cluster}` | Tables in one cluster |
| `clickhouse://tables/{cluster}/{database}` | Tables in one database of a cluster |
| `clickhouse://tables/{cluster}/{database}/{table}` | Table schema details |
| `python://ethpandaops` | Python library API docs |

```
search_examples(query="block arrival time")
search_runbooks(query="network not finalizing")
execute_python(code="...")
manage_session(operation="list")
```

## The ethpandaops Python Library

### ClickHouse - Blockchain Data

```python
from ethpandaops import clickhouse

# List available clusters
clusters = clickhouse.list_datasources()
# Returns: [{"name": "xatu", "database": "default"}, {"name": "xatu-cbt", ...}]

# Query data (returns pandas DataFrame)
df = clickhouse.query("xatu-cbt", """
    SELECT
        slot,
        avg(seen_slot_start_diff) as avg_arrival_ms
    FROM mainnet.fct_block_first_seen_by_node
    WHERE slot_start_date_time >= now() - INTERVAL 1 HOUR
    GROUP BY slot
    ORDER BY slot DESC
""")

# Parameterized queries
df = clickhouse.query("xatu", "SELECT * FROM blocks WHERE slot > {slot}", {"slot": 1000})
```

**Cluster selection:**
- `xatu-cbt` - Pre-aggregated tables (faster, use for metrics)
- `xatu` - Raw event data (use for detailed analysis)

**Required filters:**
- ALWAYS filter on partition key: `slot_start_date_time >= now() - INTERVAL X HOUR`
- Filter by network: `meta_network_name = 'mainnet'` or use schema like `mainnet.table_name`

### Prometheus - Infrastructure Metrics

```python
from ethpandaops import prometheus

# List instances
instances = prometheus.list_datasources()

# Instant query
result = prometheus.query("ethpandaops", "up")

# Range query
result = prometheus.query_range(
    "ethpandaops",
    "rate(http_requests_total[5m])",
    start="now-1h",
    end="now",
    step="1m"
)
```

**Time formats:** RFC3339 or relative (`now`, `now-1h`, `now-30m`)

### Logs — OTel ClickHouse (`external.otel_logs`)

On ethpandaops infra, container logs from hosted devnets and platform services are shipped via OpenTelemetry into the `clickhouse-raw` cluster, database `external`, table `external.otel_logs`. Query them with SQL through the `clickhouse` module. **For ethpandaops devnets, expect ClickHouse logs, not Loki** — a `loki` module exists for deployments that advertise one (check `panda datasources`), but it's not used here. (Local Kurtosis logs are separate: use the `local-kurtosis` datasource / `otel.otel_logs`.)

```python
from ethpandaops import clickhouse

# Devnet logs are keyed by ResourceAttributes['network'] and ResourceAttributes['host.name'].
# ALWAYS filter on Timestamp (the partition key) and network. Use a RAW string (r""")
# so regex escapes (\b, \x1b) reach ClickHouse intact instead of becoming Python control chars.
# `clean` strips ANSI colour codes so the severity anchors below see the real LEVEL token.
df = clickhouse.query("clickhouse-raw", r"""
    WITH replaceRegexpAll(Body, '\x1b\[[0-9;?]*[A-Za-z]', '') AS clean
    SELECT Timestamp, ResourceAttributes['host.name'] AS host, clean AS Body
    FROM external.otel_logs
    WHERE ResourceAttributes['network'] = {network:String}
      AND ResourceAttributes['host.name'] = {host:String}   -- keep the strip query bounded (see warning below)
      -- error-class LEVEL token only (see "Severity triage" below) — never a bare substring
      AND match(clean, '(^|[][ |])(CRIT|ERRO|ERROR|FATAL|PANIC)($|[][ |:])|^(ERR|FAT)\b|\blevel=(crit|error|fatal|panic)\b')
      AND NOT match(clean, '(^|[][ |])(DEBUG|DBG|TRACE|TRC)($|[][ |:])|\blevel=(debug|trace)\b')
      AND Timestamp >= now() - INTERVAL 1 HOUR
    ORDER BY Timestamp DESC
    LIMIT 200
""", parameters={"network": "fusaka-devnet-0", "host": "lighthouse-geth-super-1"})
```

**Schema (key fields):**
- `Timestamp DateTime64(9)` — partition key; always filter on it.
- `Body String` — the raw log line. The level is usually embedded here. **Lines are terminal-coloured: the level token is wrapped in ANSI escape codes** (`\x1b[31mERROR\x1b[0m`), which defeat severity anchoring and corrupt the `idx_body` token index — strip them with a `clean` CTE on *bounded* queries (see "Severity triage").
- `SeverityText` — often EMPTY for raw Docker logs; do not rely on it. Match severity on the ANSI-stripped `Body` instead (see "Severity triage").
- `ServiceName` — empty for VM/Docker devnet logs (the `k8s.*` materialized columns apply only to Kubernetes platform logs).
- `ResourceAttributes Map(String, String)` — node identity: `network` (devnet name), `host.name` (the node, e.g. `lighthouse-geth-super-1`), `ingress_user`, `deployment.environment`.
- `LogAttributes Map(String, String)` — per-line attributes: `log.file.name` / `log.file.path` (one json-log file per container on the node), `container_id`, plus structured fields the client emits (`level`, `msg`, ...).

**Discover what to filter on:**
```python
# Networks currently shipping logs
clickhouse.query("clickhouse-raw", """
    SELECT DISTINCT ResourceAttributes['network'] AS network
    FROM external.otel_logs
    WHERE Timestamp >= now() - INTERVAL 1 HOUR
""")

# Nodes (host.name) in a network
clickhouse.query("clickhouse-raw", """
    SELECT DISTINCT ResourceAttributes['host.name'] AS host
    FROM external.otel_logs
    WHERE ResourceAttributes['network'] = {network:String}
      AND Timestamp >= now() - INTERVAL 1 HOUR
    ORDER BY host
""", parameters={"network": "fusaka-devnet-0"})
```

**Node naming:** `host.name` is `<cl>-<el>-<tier>-<n>` (e.g. `lighthouse-geth-super-1` → CL lighthouse, EL geth); bootnodes and MEV relays don't follow it. There is **no `ethereum_cl` / `ethereum_el` field** — a node runs the CL, EL, validator and sidecar containers together, separated only by `LogAttributes['log.file.name']`. Filter `host.name LIKE 'lighthouse-%'` to sweep lighthouse-CL nodes, or isolate one client by discovering its `log.file.name` (and a sample of its `Body`) and filtering on it.

**Severity triage — strip ANSI, then anchor on the LEVEL token (never a bare substring).** `SeverityText` is empty, so severity comes from `Body` — but `Body` is terminal-coloured. Strip the colour codes in a `clean` CTE, then match the level token on `clean`. This avoids two traps: a bare `(?i)error` matches tens of thousands of benign DEBUG lines on a healthy network; and an un-stripped `\x1b[31mERROR\x1b[0m` sits flush against the escape bytes so the anchors miss it (~49% of real errors lost on a broken network).

```sql
-- bounded query only: always pair with a host filter + tight Timestamp window + LIMIT (see warning)
WITH replaceRegexpAll(Body, '\x1b\[[0-9;?]*[A-Za-z]', '') AS clean
... WHERE <network> AND <host> AND <tight time window>
  -- error-class level token (uppercase token, nimbus 3-letter, or logfmt level=)
  AND match(clean, '(^|[][ |])(CRIT|ERRO|ERROR|FATAL|PANIC)($|[][ |:])|^(ERR|FAT)\b|\blevel=(crit|error|fatal|panic)\b')
  -- never count a debug/trace line, even when it carries an err= field
  AND NOT match(clean, '(^|[][ |])(DEBUG|DBG|TRACE|TRC)($|[][ |:])|\blevel=(debug|trace)\b')
... LIMIT 200
```

- **⚠️ Bounded queries only.** Wrapping `Body` in `replaceRegexpAll` is a computed expression, so the `idx_body` skip-index can't prune granules and every scanned row is rewritten. With the primary key led by `IngressUser` (not network/host) and data >7d on S3, a stripped-`Body` regex over a wide/multi-day window becomes a full scan. Narrow to the suspect host + tight window first, then strip within that slice.
- **Raw strings.** Pass SQL as `r"""..."""` so `\b`/`\x1b` reach ClickHouse intact (a normal string turns `\b` into a backspace byte).
- **Case matters.** Levels are uppercase (`ERROR`, `CRIT`, `ERR`, `FAT`); prose "error" is lowercase — case-sensitive matching separates them. logfmt uses lowercase `level=error` (matched above).
- **Level vocab** (sample to confirm): lighthouse `… ERROR …`; geth/nethermind/erigon/reth/besu `ERROR [..]` or `…|ERROR|…`; prysm `[ts] ERROR comp:` / `level=error`; nimbus `ERR`/`FAT`/`WRN` at line start; lodestar `level=error`.
- **Escalate after the error pass.** If empty/inconclusive, add `WARN`/`WRN`/`level=warn`, then drop the filter for INFO/DEBUG (verbose — keep window + `LIMIT` tight).

For a full devnet debugging procedure, run `panda search runbooks "debug devnet"`.

### Dora - Beacon Chain Explorer

**Discovering all Dora API endpoints:**

Before using Dora, discover the full set of available API endpoints by fetching the Swagger documentation. The swagger page is always at `<dora-url>/api/swagger/index.html`.

1. First, get the Dora base URL for the network:
```python
from ethpandaops import dora
base_url = dora.get_base_url("mainnet")
print(f"Swagger docs: {base_url}/api/swagger/index.html")
```

2. Then use `WebFetch` to read the swagger page at `{base_url}/api/swagger/index.html` to discover all supported API endpoints for that Dora instance. This is important because different Dora deployments may support different endpoints.

3. Use the discovered endpoints to make targeted API calls via the Python `dora` module or direct HTTP requests.

Use `search(type="examples", query="network overview")` and `search(type="examples", query="dora")` for common API patterns.

**Direct HTTP calls for endpoints not in the Python module:**

**⚠️ Send a browser `User-Agent`.** Hosted Dora is behind Cloudflare, which 403s default Python/`httpx`/`urllib` UAs (`Error 1010`, browser-integrity) — fine in a browser, fails from the sandbox without a browser UA. (The `dora.*` module functions are unaffected — they route through the server.)

```python
from ethpandaops import dora
import httpx

base_url = dora.get_base_url("mainnet")
headers = {
    "Accept": "application/json",
    "User-Agent": "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36",
}
# Call any endpoint discovered from swagger (e.g. /forks for split detection)
with httpx.Client(timeout=30, headers=headers) as client:
    resp = client.get(f"{base_url}/api/v1/<endpoint>")
    resp.raise_for_status()
    data = resp.json()
```

### Storage - Upload Outputs

```python
from ethpandaops import storage

# Save visualization
import matplotlib.pyplot as plt
plt.savefig("/workspace/chart.png")

# Upload for public URL
url = storage.upload("/workspace/chart.png")
print(f"Chart URL: {url}")

# List uploaded files
files = storage.list_files()
```

## Session Management

**Critical:** Each execution runs in a **fresh Python process**. Variables do NOT persist.

**Files persist:** Save to `/workspace/` to share data between calls.

**Reuse sessions:** Pass `--session <id>` (CLI) or `session_id` (MCP) for faster startup and workspace persistence.

### Multi-Step Analysis Pattern

```python
# Call 1: Query and save
from ethpandaops import clickhouse
df = clickhouse.query("xatu-cbt", "SELECT ...")
df.to_parquet("/workspace/data.parquet")
```

```python
# Call 2: Load and visualize (reuse session from Call 1)
import pandas as pd
import matplotlib.pyplot as plt
from ethpandaops import storage

df = pd.read_parquet("/workspace/data.parquet")
plt.figure(figsize=(12, 6))
plt.plot(df["slot"], df["value"])
plt.savefig("/workspace/chart.png")
url = storage.upload("/workspace/chart.png")
print(f"Chart: {url}")
```

## Error Handling

ClickHouse errors include actionable suggestions:
- Missing date filter → "Add `slot_start_date_time >= now() - INTERVAL X HOUR`"
- Wrong cluster → "Use xatu-cbt for aggregated metrics"
- Query timeout → Break into smaller time windows

Default execution timeout is 60s, max 600s. For large analyses:
- Search for optimized patterns first (`panda search examples "..."`)
- Break work into smaller time windows
- Save intermediate results to `/workspace/`

## Notes

- Always filter ClickHouse queries on partition keys (`slot_start_date_time`)
- Use `xatu-cbt` for pre-aggregated metrics, `xatu` for raw event data
- Use `panda docs` or `python://ethpandaops` resource for complete API documentation
- Search for examples before writing complex queries from scratch
- Search for runbooks to find common investigation workflows
- Upload visualizations with `storage.upload()` for shareable URLs
- NEVER just copy/paste/recite base64 of images. You MUST save the image to the workspace and upload it to give it back to the user.
