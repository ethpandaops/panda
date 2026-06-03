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

Container logs from hosted devnets and platform services are shipped via OpenTelemetry into the `clickhouse-raw` ClickHouse cluster, database `external`, table `external.otel_logs`. Query them with SQL through the `clickhouse` module — **there is no Loki datasource**. (Local Kurtosis devnet logs are separate: query the autodiscovered `local-kurtosis` datasource / `otel.otel_logs` instead.)

```python
from ethpandaops import clickhouse

# Devnet logs are keyed by ResourceAttributes['network'] and ResourceAttributes['host.name'].
# ALWAYS filter on Timestamp (the partition key) and network.
df = clickhouse.query("clickhouse-raw", """
    SELECT Timestamp, ResourceAttributes['host.name'] AS host, Body
    FROM external.otel_logs
    WHERE ResourceAttributes['network'] = {network:String}
      AND match(Body, '(?i)(crit|err|error|fatal)')
      AND Timestamp >= now() - INTERVAL 1 HOUR
    ORDER BY Timestamp DESC
    LIMIT 200
""", parameters={"network": "fusaka-devnet-0"})
```

**Schema (key fields):**
- `Timestamp DateTime64(9)` — partition key; always filter on it.
- `Body String` — the raw log line. The level is usually embedded here.
- `SeverityText` — often EMPTY for raw Docker logs; do not rely on it. Match severity on `Body` instead.
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

**Log level formats vary by client.** `SeverityText` is unreliable here, so triage on `Body`:
- Start with `match(Body, '(?i)(crit|err|error|fatal)')`.
- Broaden to include `warn`, then drop the severity filter for INFO/DEBUG (verbose — keep a tight time window and a `LIMIT`).
- Client formats differ — lighthouse `MMM DD HH:MM:SS.mmm LEVEL`, geth `LEVEL [MM-DD|HH:MM:SS.mmm]`, prysm `level=... msg=...`. Sample a few unfiltered lines to confirm a client's format before crafting a precise regex.

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

```python
from ethpandaops import dora
import httpx

base_url = dora.get_base_url("mainnet")
# Call any endpoint discovered from swagger
with httpx.Client(timeout=30) as client:
    resp = client.get(f"{base_url}/api/v1/<endpoint>")
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
