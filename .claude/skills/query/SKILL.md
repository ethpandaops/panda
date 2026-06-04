---
name: query
description: Query Ethereum network data via ethpandaops CLI or MCP server. Use when analyzing blockchain data, block timing, attestations, validator performance, network health, or infrastructure metrics. Provides access to ClickHouse (blockchain data and OTel logs), Prometheus (metrics), and Dora (explorer APIs).
argument-hint: <query or question>
user-invocable: false
---

# ethpandaops Query Guide

This skill is a router. The full, always-current usage guide is **owned by the code** —
read it first and follow it:

- **CLI:** `panda getting-started`
- **MCP:** read the `panda://getting-started` resource

It is generated live from the running server — workflow, the Python
`clickhouse`/`prometheus`/`dora`/`storage` API, the ClickHouse raw-vs-refined rules,
partition-key filters, sessions, and storage — so it never goes stale. Use
`panda docs <module>` for a module's full API and `panda search examples "<topic>"` for
worked queries. Everything below is the durable context that guide does not carry.

## Discover names — don't hardcode them

Datasource, cluster, and table names are owned by the proxy and change over time, so
enumerate them from the live tooling rather than pasting a name from memory, an old
chat, or a screenshot:

```bash
panda datasources                                 # datasources and their types
panda schema [<cluster> [<database> [<table>]]]   # clusters → tables → schema
```

The embedded examples and docs (`panda search examples`, `panda docs`) are compiled from
the current binary, so the names in their output are current too — trust those.

## Search before writing queries

Working query patterns live in the embedded examples and runbooks:

```bash
panda search examples "block arrival time"
panda search runbooks "finality delay"
```

## Logs are in ClickHouse, not Loki

Container logs from hosted devnets and platform services ship via OpenTelemetry into
ClickHouse (`external.otel_logs`) — there is no hosted Loki datasource. For the schema
and the full procedure: `panda search runbooks "debug devnet"`. (Local Kurtosis devnet
logs are the separate autodiscovered `local-kurtosis` datasource.)

## Notes

- Prefer the CLI (`panda` binary); use the MCP tools (`execute_python`,
  `manage_session`, `search`) only if they appear in your tool list.
- Each execution is a fresh Python process — variables do not persist, but `/workspace/`
  files do. Default timeout 60s, max 600s.
- **NEVER recite or paste base64 image data.** Save the image to `/workspace/` and
  `storage.upload()` it to return it to the user.
