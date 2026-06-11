CBT-transformed, pre-aggregated tables.

- **Table syntax:** `FROM {network}.table_name` — there is one database per network (`mainnet`, `holesky`, …), and the database prefix **is** the network filter.
- **Use `FINAL`** to read the merged/deduplicated rows.
- **Always filter the partition column** (usually `slot_start_date_time`) to avoid timeouts.
- **Canonical vs head:** finalized tables have a `_canonical` variant (no reorgs, for historical analysis); live tables have a `_head` variant (may reorg, for real-time monitoring) — e.g. `fct_block_canonical` vs `fct_block_head`.
