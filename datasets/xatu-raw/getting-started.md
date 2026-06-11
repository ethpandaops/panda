Raw, unaggregated event data — one row per observation.

- **Table syntax:** `FROM table_name` (single database).
- **Network filter:** `WHERE meta_network_name = 'mainnet'`.
- **Always filter the partition column** (usually `slot_start_date_time`) to avoid timeouts.
- **Finalized vs live:** `canonical_beacon_*` tables are finalized (no reorgs); `beacon_api_*` and `libp2p_*` tables are live observations and may include reorged data.
