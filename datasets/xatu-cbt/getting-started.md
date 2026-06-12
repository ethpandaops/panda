CBT-transformed, pre-aggregated tables.

Every table here is built by the CBT pipeline and only contains what the
pipeline has processed so far — coverage is not all-of-history and differs per
table and network.

- **Check coverage first:** before querying any table — and always before
  concluding data is missing or scanning for MIN/MAX timestamps — get its
  processed range from the pipeline:
  `cbt.get_transformation_coverage(network, "{network}.<table>")` returns the
  processed `(position, interval)` ranges (slot-type positions are unix
  seconds; multiple ranges mean gaps), and
  `cbt.debug_coverage(network, id, position)` explains an unprocessed position
  (dependency bounds, gaps). An empty query result can mean "not yet
  transformed", not "didn't happen".
- **Table syntax:** `FROM {network}.table_name` — there is one database per network (`mainnet`, `holesky`, …), and the database prefix **is** the network filter.
- **Use `FINAL`** to read the merged/deduplicated rows.
- **Always filter the partition column** (usually `slot_start_date_time`) to avoid timeouts.
- **Canonical vs head:** finalized tables have a `_canonical` variant (no reorgs, for historical analysis); live tables have a `_head` variant (may reorg, for real-time monitoring) — e.g. `fct_block_canonical` vs `fct_block_head`.
