---
name: Check Devnet Health with Prometheus
description: Read the metrics lane of devnet health — discover Prometheus datasources and metric names (prometheus.list_datasources, get_label_values on __name__), scope every selector to the shared devnets datasource's network label (network="<devnet>"), inspect labels before filtering, then run bounded PromQL (query, query_range) for service liveness (up), peer counts, restarts, and CPU/memory/disk pressure. Use when a devnet question is about metrics — is a node scraped and up, how many peers, is a service resource-starved — rather than chain state or logs.
tags: [prometheus, promql, metrics, devnet, health, monitoring]
triggers:
  - check devnet node metrics with prometheus
  - peer count cpu memory disk metrics for devnet nodes
  - is a service up or restarting according to metrics
  - which prometheus metrics exist for this client
  - promql query for devnet health over a time range
prerequisites: [prometheus]
---

Owns the METRICS lane of devnet health: Prometheus datasource and metric discovery,
and bounded PromQL for liveness, peering, and resource pressure. Chain-level verdicts
(finality, participation thresholds) belong to `runbooks://ethereum_protocol_model`;
the log lane to the OTel tables (`runbooks://kurtosis_devnet` locally); the overall
debugging procedure to `runbooks://debug_ethereum_network`.

## Inputs
Required: the question (which service, which signal) and a time window.
Preferred: the Prometheus datasource name and the client/service labels in play —
otherwise discover both first.

## Output
Cited metric readings and what they support — each reading is an evidence item
(source = the datasource, ref = the exact PromQL and window) per
`runbooks://evidence_discipline`. Example:

```yaml
- source: prometheus
  ref: "query_range(ds, 'up{network=\"glamsterdam-devnet-6\",job=\"beacon\"}', '30s', start='2026-07-01T10:40:00Z', end='2026-07-01T11:40:00Z')"
  at: "2026-07-01T10:40Z-11:40Z"
  detail: "cl-3 target up==0 from 10:41Z; all other targets up==1"
```

## Procedure

Python below is a shape to adapt — substitute the datasource, terms, and window.

1. **Discover the datasource, then the network label.** Never assume one exists or
   guess its name — list them first. Public devnets are NOT one datasource per
   network: they share a single datasource (commonly `devnets`) that multiplexes many
   networks behind a `network` label, so selecting the datasource is only half the
   job — every selector must also carry `network="<devnet>"`, or the reading silently
   mixes networks. If more than one datasource plausibly matches, stop and surface the
   candidates.

   ```python
   from ethpandaops import prometheus
   names = [d["name"] for d in prometheus.list_datasources()]
   datasource = "devnets"                                   # the shared devnet datasource; stop if ambiguous
   network = "glamsterdam-devnet-6"                          # confirm it exists as a label value:
   prometheus.get_label_values(datasource, "network", contains=network, limit=20)
   ```

2. **Discover metric names before writing PromQL.** Client metric names differ per
   client and version — filter `__name__` by the concept, don't recall names from
   memory:

   ```python
   prometheus.get_label_values(datasource, "__name__", contains="peer", limit=50)
   ```

3. **Inspect labels before filtering** so selectors use labels that exist in this
   deployment: `prometheus.get_labels(datasource)`, then
   `prometheus.get_label_values(datasource, "<label>")`.

4. **Query bounded.** `prometheus.query(datasource, promql)` for current state;
   `prometheus.query_range(datasource, promql, step, start=start, end=end)` for
   trends — `step` comes before the window bounds, and `start`/`end` take RFC3339
   timestamps or `now`-relative forms like `"now-1h"` (a bare offset like `"-1h"` is
   rejected); prefer RFC3339 in citations so the evidence re-derives. Keep the window to the
   incident, and search the examples index for "prometheus" query patterns rather
   than inventing them.

5. **Start from `up`.** It is the universal scrape-liveness signal, but on the shared
   datasource always scope it: `up{network="<devnet>"}` — a bare `up` mixes every
   network on the datasource. `up == 0` means the target failed scraping, and a
   missing series means the target isn't configured or the metric never existed.
   Counter resets on `process_*`/uptime-style metrics indicate restarts. `up` (and
   other universal metrics) are known signals — you don't need to rediscover them, and
   a `contains="up"` filter buries the exact `up` among many names that contain it.

## Rules

- **A scrape gap mimics downtime.** A target absent from `up` or a series that stops
  is evidence about scraping/shipping, not proof the service is down — verify the
  service directly (its API or logs) before calling it dead, the same way an empty
  log tail is not proof of silence (`runbooks://kurtosis_devnet`).
- **Metrics support, they don't convict.** Resource pressure or peer loss coinciding
  with a chain symptom is a hypothesis input for
  `runbooks://debug_ethereum_network`, not a root cause by itself; chain-health
  verdicts still come from checkpoints and participation
  (`runbooks://ethereum_protocol_model`).
- Cap high-cardinality queries (per-validator, per-peer labels) with `topk` or
  aggregation before plotting.

## Self-Check

Before returning:
- Every metric name used was discovered from the datasource, not recalled.
- Every reading carries the exact PromQL + window that re-derives it.
- Absent series were reported as scrape/coverage gaps, not downtime.
