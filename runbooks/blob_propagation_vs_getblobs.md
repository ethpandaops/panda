---
name: Blob Propagation vs engine_getBlobs Success
description: Investigate whether blob gossip propagation timing affects engine_getBlobs success rates
tags: [blobs, engine_api, gossipsub, propagation, da]
prerequisites: [clickhouse-raw, clickhouse-refined]
---

This runbook investigates the relationship between blob propagation via gossipsub and engine_getBlobs success rates. Because the data lives on two different clusters, you run separate queries and merge in Python.

## Data Sources

| Metric | Table | Cluster | Join Key |
|--------|-------|---------|----------|
| getBlobs success/empty rates | `fct_engine_get_blobs_by_slot` | clickhouse-refined | `slot` |
| Blob gossip propagation timing | `libp2p_gossipsub_blob_sidecar` | clickhouse-raw | `slot` |

## Steps

### 1. Get engine_getBlobs status breakdown per slot

```python
from ethpandaops import clickhouse

getblobs = clickhouse.query("clickhouse-refined", """
    SELECT
        slot,
        status,
        observation_count,
        avg_duration_ms,
        full_return_pct
    FROM {network}.fct_engine_get_blobs_by_slot FINAL
    WHERE slot_start_date_time >= now() - INTERVAL 1 HOUR
    ORDER BY slot DESC
""")
print(getblobs)
```

The `status` column contains values like `SUCCESS` and `EMPTY`. `full_return_pct` shows what fraction of nodes got all requested blobs back.

### 2. Get blob gossip propagation timing per slot

```python
propagation = clickhouse.query("clickhouse-raw", """
    SELECT
        slot,
        AVG(propagation_slot_start_diff) AS avg_blob_propagation_ms,
        quantile(0.95)(propagation_slot_start_diff) AS p95_blob_propagation_ms,
        COUNT() AS blob_messages
    FROM libp2p_gossipsub_blob_sidecar
    WHERE meta_network_name = '{network}'
        AND slot_start_date_time >= now() - INTERVAL 1 HOUR
    GROUP BY slot
    ORDER BY slot DESC
""")
print(propagation)
```

### 3. Merge and correlate

```python
import pandas as pd

merged = pd.merge(getblobs, propagation, on="slot", how="inner")

# Compare propagation timing for SUCCESS vs EMPTY getBlobs
for status, group in merged.groupby("status"):
    print(f"\n{status}:")
    print(f"  avg blob propagation: {group['avg_blob_propagation_ms'].mean():.0f}ms")
    print(f"  p95 blob propagation: {group['p95_blob_propagation_ms'].mean():.0f}ms")
    print(f"  slots: {len(group)}")
```

## What to look for

- Slots where getBlobs returns `EMPTY` should correlate with slower blob gossip propagation — blobs hadn't arrived via P2P yet when the EL called getBlobs.
- High `p95_blob_propagation_ms` with low `full_return_pct` indicates the mempool isn't filling fast enough for the engine API to benefit.
