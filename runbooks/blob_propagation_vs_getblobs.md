---
name: Correlate Blob Propagation with engine_getBlobs
description: Investigate whether slow blob/data-column gossip propagation is causing engine_getBlobs to return EMPTY — join per-slot getBlobs success rates (refined fct_engine_get_blobs_by_slot) with gossip timing (raw libp2p_gossipsub_data_column_sidecar on PeerDAS networks, libp2p_gossipsub_blob_sidecar pre-Fulu; propagation_slot_start_diff) and compare timing across the SUCCESS vs EMPTY groups. Use when getBlobs empties or blob availability looks timing-related.
tags: [blobs, engine-api, gossipsub, propagation, data-availability]
triggers:
  - engine_getBlobs returning empty
  - blob or data column propagation slow via gossip
  - correlate getblobs success with blob arrival timing
  - join fct_engine_get_blobs_by_slot with libp2p_gossipsub_data_column_sidecar
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
`runbooks://clickhouse_querying` for cluster/partition rules, dataset placement (the
raw datasource differs per network), and why the gossip table must be queried raw
(deduplicated views collapse the propagation rows). The Python below is a shape to
adapt — substitute `{network}`, the raw datasource, and the time window, and verify
each side has rows in the window before merging.

1. **getBlobs status per slot** — refined `{network}.fct_engine_get_blobs_by_slot
   FINAL`. The table's grain includes `node_class`, so aggregate to one row per
   `(slot, status)`; `full_return_pct` is the fraction of nodes that got all blobs.

   ```python
   from ethpandaops import clickhouse
   getblobs = clickhouse.query("clickhouse-refined", """
       SELECT slot, status,
              -- alias must differ from the weight column: reusing the name makes
              -- ClickHouse substitute the sum() into avgWeighted (ILLEGAL_AGGREGATION)
              sum(observation_count) AS total_observations,
              avgWeighted(avg_duration_ms, observation_count) AS avg_duration_ms,
              avgWeighted(full_return_pct, observation_count) AS full_return_pct
       FROM {network}.fct_engine_get_blobs_by_slot FINAL
       WHERE slot_start_date_time >= now() - INTERVAL 1 HOUR
         AND status IN ('SUCCESS', 'EMPTY')
       GROUP BY slot, status
   """)
   ```

2. **Gossip timing per slot** — pick the propagation table by the network's DA era:
   on Fulu/PeerDAS networks blobs travel as data columns, so use
   `libp2p_gossipsub_data_column_sidecar`; use `libp2p_gossipsub_blob_sidecar` only
   pre-Fulu (on current networks it is historical and goes quiet after the fork).
   Filter `meta_network_name` and both time columns — these tables partition on
   `(meta_network_name, toYYYYMM(event_date_time))`, so the `event_date_time`
   predicate is what prunes partitions:

   ```python
   propagation = clickhouse.query("<raw-datasource-per-placement>", """
       SELECT slot,
              AVG(propagation_slot_start_diff) AS avg_ms,
              quantile(0.95)(propagation_slot_start_diff) AS p95_ms,
              COUNT() AS gossip_messages
       FROM libp2p_gossipsub_data_column_sidecar
       WHERE meta_network_name = '{network}'
         AND event_date_time >= now() - INTERVAL 1 HOUR
         AND slot_start_date_time >= now() - INTERVAL 1 HOUR
       GROUP BY slot
   """)
   ```

3. **Merge on `slot`, compare timing by status** — `pd.merge(getblobs, propagation,
   on="slot")`, then group by `status` and compare `avg_ms`/`p95_ms`. If the merge is
   empty or a cohort (`SUCCESS` or `EMPTY`) is absent in the window, report the
   analysis as inconclusive and name the missing side — do not force a conclusion
   from one-sided data.

## What to look for
- EMPTY getBlobs correlating with slower gossip propagation → blobs hadn't arrived via
  P2P when the EL called getBlobs.
- High p95 propagation with low `full_return_pct` → the mempool isn't filling fast enough
  for the engine API to benefit.
- No EMPTY rows at all in the window is itself an answer: getBlobs is not
  timing-starved right now.
