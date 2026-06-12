CBT-transformed, pre-aggregated tables.

- **Table syntax:** `FROM {network}.table_name` — there is one database per network (`mainnet`, `holesky`, …), and the database prefix **is** the network filter.
- **Use `FINAL`** to read the merged/deduplicated rows.
- **Always filter the partition column** (usually `slot_start_date_time`) to avoid timeouts.
- **Canonical vs head:** finalized tables have a `_canonical` variant (no reorgs, for historical analysis); live tables have a `_head` variant (may reorg, for real-time monitoring) — e.g. `fct_block_canonical` vs `fct_block_head`.
- **Check coverage before concluding data is missing:** the `cbt` module queries the pipeline that builds these tables — `cbt.get_transformation_coverage(network, "{network}.fct_block")` returns the processed ranges, and `cbt.debug_coverage(network, id, position)` explains why a position is unprocessed (dependency bounds, gaps). An empty query result can mean "not yet transformed", not "didn't happen".
