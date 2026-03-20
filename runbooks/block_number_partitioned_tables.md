---
name: Querying Block-Number-Partitioned Tables
description: How to efficiently query xatu-cbt tables partitioned by block_number instead of slot_start_date_time
tags: [clickhouse, performance, execution, block_number, partitioning]
prerequisites: [xatu-cbt]
---

Some xatu-cbt tables are partitioned by `block_number` instead of `slot_start_date_time`. You MUST filter on `block_number` to avoid full table scans.

## Which tables use block_number partitioning?

Execution trace tables:
- `int_transaction_call_frame` — call frame gas and call tree analysis
- `int_transaction_call_frame_opcode_gas` — per-opcode gas within each frame
- `int_transaction_call_frame_opcode_resource_gas` — resource-level gas breakdown

If a table has `block_number` but no `slot_start_date_time`, it is partitioned by `block_number`.

## How to query a time range

To query these tables for a time window (e.g. "last 24 hours"), you MUST first resolve the block number range from `fct_block_head`, then use that range:

```python
from ethpandaops import clickhouse

# Step 1: Get block number range for your time window
block_range = clickhouse.query("xatu-cbt", """
    SELECT
        MIN(execution_payload_block_number) AS min_block,
        MAX(execution_payload_block_number) AS max_block
    FROM {network}.fct_block_head FINAL
    WHERE slot_start_date_time >= now() - INTERVAL 24 HOUR
""")

min_block = block_range['min_block'][0]
max_block = block_range['max_block'][0]

# Step 2: Use the block range to query execution trace tables
result = clickhouse.query("xatu-cbt", f"""
    SELECT
        target_address,
        sum(gas) AS total_gas,
        count() AS call_count
    FROM {{network}}.int_transaction_call_frame
    WHERE block_number BETWEEN {min_block} AND {max_block}
      AND target_address IS NOT NULL
    GROUP BY target_address
    ORDER BY total_gas DESC
    LIMIT 20
""")
```

## Important

- NEVER use `SELECT max(block_number) FROM ...` as a subquery — this causes a full table scan on the block-number-partitioned table itself.
- NEVER filter only on `updated_date_time` — it is not the partition key and will scan all partitions.
- Always resolve block numbers from `fct_block_head` first, then pass them as literal values.
- For single-block queries, just use `WHERE block_number = <number>` directly.
