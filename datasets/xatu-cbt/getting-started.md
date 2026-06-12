CBT-transformed, pre-aggregated tables.

- **Table syntax:** `FROM {network}.table_name` — there is one database per network (`mainnet`, `holesky`, …), and the database prefix **is** the network filter.
- **Use `FINAL`** to read the merged/deduplicated rows.
- **Always filter the partition column** (usually `slot_start_date_time`) to avoid timeouts.
- **Canonical vs head:** finalized tables have a `_canonical` variant (no reorgs, for historical analysis); live tables have a `_head` variant (may reorg, for real-time monitoring) — e.g. `fct_block_canonical` vs `fct_block_head`.
- **Attestation correctness tables:** `fct_attestation_correctness_by_validator_*` rows are scheduled validator attestation duties with an outcome. A plain `COUNT()` counts duties, including missed rows. For "made", "included", or "actual" attestations, filter to the relevant outcome first (for example `status != 'missed'` for non-missed canonical outcomes, `status = 'canonical'` for canonical-chain included attestations, or non-null distance fields on `_head`) or use liveness/first-seen tables whose count columns explicitly separate attestations from missed duties.
- **Inspect outcomes before aggregating:** when a table has outcome columns such as `status`, `slot_distance`, `propagation_distance`, or `inclusion_distance`, check the status/null breakdown before turning row counts into an answer.
