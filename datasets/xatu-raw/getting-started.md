Raw, unaggregated event data — one row per observation.

- **Table syntax:** public networks: `FROM table_name` (the `default` database);
  devnets: ``FROM `<network>`.table_name`` (one database per devnet, e.g.
  ``glamsterdam-devnet-6``).
- **Network filter:** always `WHERE meta_network_name = '<network>'` — it leads the
  primary key in both layouts.
- **Always filter the partition column** (usually `slot_start_date_time`) to avoid timeouts.
- **Finalized vs live:** `canonical_beacon_*` tables are finalized (no reorgs); `beacon_api_*` and `libp2p_*` tables are live observations and may include reorged data.
- **Check availability before concluding data is missing:** retention differs per table. For tables the CBT pipeline consumes, `cbt.get_external_bounds(network)` returns each table's min/max available positions without scanning it.
