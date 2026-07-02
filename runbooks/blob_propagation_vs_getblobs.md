---
name: Correlate Blob Propagation with engine_getBlobs
description: Investigate whether slow blob gossip propagation is causing engine_getBlobs to return EMPTY — join per-slot getBlobs success rates (refined) with blob gossip timing (raw) and compare timing across the SUCCESS vs EMPTY groups. Use when getBlobs empties or blob availability looks timing-related.
tags: [blobs, engine-api, gossipsub, propagation, data-availability]
triggers:
  - engine_getBlobs returning empty
  - blob propagation slow via gossip
  - correlate getblobs success with blob arrival timing
prerequisites: [clickhouse-raw, clickhouse-refined]
---

Correlate blob gossip propagation timing with engine_getBlobs success. Reference when
getBlobs returns EMPTY or blob availability looks propagation-bound.

## Inputs
Required: the network and a time window.

## Output
Whether EMPTY getBlobs correlates with slower blob propagation, with the per-status
timing comparison behind it.

## Procedure
The data lives on two clusters, joined in Python on `slot`. See
`runbooks://clickhouse_querying` for cluster/partition rules and why the gossip table
must be queried raw (deduplicated views collapse the propagation rows).

1. **getBlobs status per slot** — refined `{network}.fct_engine_get_blobs_by_slot FINAL`:
   `slot, status, observation_count, avg_duration_ms, full_return_pct` where `status ∈
   {SUCCESS, EMPTY}` and `full_return_pct` is the fraction of nodes that got all blobs.

   ```python
   from ethpandaops import clickhouse
   getblobs = clickhouse.query("clickhouse-refined", """
       SELECT slot, status, observation_count, avg_duration_ms, full_return_pct
       FROM {network}.fct_engine_get_blobs_by_slot FINAL
       WHERE slot_start_date_time >= now() - INTERVAL 1 HOUR
   """)
   ```

2. **Blob gossip timing per slot** — raw `libp2p_gossipsub_blob_sidecar`, filtered by
   `meta_network_name`:

   ```python
   propagation = clickhouse.query("clickhouse-raw", """
       SELECT slot,
              AVG(propagation_slot_start_diff) AS avg_ms,
              quantile(0.95)(propagation_slot_start_diff) AS p95_ms,
              COUNT() AS blob_messages
       FROM libp2p_gossipsub_blob_sidecar
       WHERE meta_network_name = '{network}'
         AND slot_start_date_time >= now() - INTERVAL 1 HOUR
       GROUP BY slot
   """)
   ```

3. **Merge on `slot`, compare timing by status** — `pd.merge(getblobs, propagation,
   on="slot")`, then group by `status` and compare `avg_ms`/`p95_ms`.

## What to look for
- EMPTY getBlobs correlating with slower gossip propagation → blobs hadn't arrived via
  P2P when the EL called getBlobs.
- High p95 propagation with low `full_return_pct` → the mempool isn't filling fast enough
  for the engine API to benefit.
