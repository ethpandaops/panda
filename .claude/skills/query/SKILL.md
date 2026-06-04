---
name: query
description: Query Ethereum network data via ethpandaops CLI or MCP server. Use when analyzing blockchain data, block timing, attestations, validator performance, network health, or infrastructure metrics. Provides access to ClickHouse (blockchain data and OTel logs), Prometheus (metrics), and Dora (explorer APIs).
argument-hint: <query or question>
user-invocable: false
---

# ethpandaops Query Guide

Query Ethereum network data through the ethpandaops tools: a Python sandbox with the
`clickhouse`, `prometheus`, `dora`, and `storage` modules, driven from the `panda` CLI
or the MCP server.

This skill is a router. **The canonical, always-current usage guide is owned by the
code** — read it first and follow it:

- **CLI:** `panda getting-started`
- **MCP:** read the `panda://getting-started` resource

That guide is generated live from the running server, so its datasource names, schemas,
and worked examples are never stale. Everything below is the durable context the guide
does not carry.

## Never hardcode datasource names

Cluster, datasource, and table names are owned by the proxy and discovered at runtime —
they change. **Do not trust any name written in prose (older examples, screenshots, or
memory). Discover them every time:**

```bash
panda datasources                            # all datasources and their types
panda datasources --type clickhouse
panda schema                                 # every ClickHouse cluster and its tables
panda schema <cluster>                       # tables in one cluster
panda schema <cluster> <database> <table>    # full schema for one table
panda docs                                   # Python API modules
panda docs clickhouse                        # one module's API
```

All commands accept `--json`. In MCP, the equivalents are the `datasources://*` and
`clickhouse://tables/*` resources and `python://ethpandaops`.

## Access: CLI vs MCP

Prefer the CLI (`panda` binary) — it is always available. Use the MCP tools
(`execute_python`, `manage_session`, `search`) only if they appear in your tool list;
otherwise use `panda execute`, `panda session`, and `panda search` via Bash.

## Find patterns before writing queries

Search the embedded examples and runbooks instead of writing complex queries from
scratch — that is where current, working query patterns live:

```bash
panda search examples "block arrival time"
panda search runbooks "finality delay"
```

(MCP: `search(type="examples", query="...")`, `search(type="runbooks", query="...")`.)

## ClickHouse: refined vs raw, and required filters

ClickHouse data is split across two kinds of cluster — **get the exact names from
`panda datasources`**, never assume them:

- **refined / pre-aggregated** — `fct_*` fact tables, fastest for metrics and
  dashboards. Query with `FINAL`; scope the network via the database (e.g.
  `mainnet.fct_...`) or a `{network}` placeholder.
- **raw** — per-event tables for detailed analysis. Scope the network with
  `meta_network_name = 'mainnet'`.

**Always filter on the partition key** (`slot_start_date_time >= now() - INTERVAL X
HOUR`) and the network — unfiltered queries scan everything and time out. Prefer the
refined fact tables when both kinds can answer the question.

## Logs are in ClickHouse, not Loki

Container logs from hosted devnets and platform services ship via OpenTelemetry into
ClickHouse (`external.otel_logs`) — **there is no hosted Loki datasource**. For the
schema, node-naming conventions, and the full debugging procedure, use the runbook:

```bash
panda search runbooks "debug devnet"
```

(Local Kurtosis devnet logs are separate — the autodiscovered `local-kurtosis`
datasource / `otel.otel_logs`.)

## Dora: discover endpoints from swagger

Different Dora deployments expose different endpoints. Get the base URL, then read its
swagger to discover what is actually available:

```python
from ethpandaops import dora
base_url = dora.get_base_url("mainnet")
# WebFetch {base_url}/api/swagger/index.html to list endpoints, then call them via the
# dora module or httpx. dora.link_*() helpers generate explorer links.
```

`panda search examples "dora"` has common patterns.

## Sessions, workspace, and outputs

Each execution is a **fresh Python process** — variables do not persist; **files in
`/workspace/` do**. Reuse a session (`panda execute --session <id>`, or `session_id` in
MCP) to keep the workspace and start faster. Default timeout is 60s, max 600s — break
large analyses into time windows and save intermediate results to `/workspace/`.

Upload outputs for shareable public URLs:

```python
from ethpandaops import storage
url = storage.upload("/workspace/chart.png")
```

**NEVER recite or paste base64 image data.** Save the image to `/workspace/` and
`storage.upload()` it to return it to the user.
