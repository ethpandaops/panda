Raw, unaggregated event data — one row per observation.

- **Mainnet/testnets** (mainnet, sepolia, hoodi): the `default` database — `FROM table_name`, filter with `WHERE meta_network_name = 'mainnet'`.
- **Devnets:** one database per network on clickhouse-raw, named after the network — backtick-quote the prefix, e.g. ``FROM `blob-devnet-0`.beacon_api_eth_v1_events_block``. Same tables and schema as `default`.
- **Always filter the partition column** (usually `slot_start_date_time`) to avoid timeouts.
- **Finalized vs live:** `canonical_beacon_*` tables are finalized (no reorgs); `beacon_api_*` and `libp2p_*` tables are live observations and may include reorged data.
- **Check availability before concluding data is missing:** retention differs per table. For tables the CBT pipeline consumes, `cbt.get_external_bounds(network)` returns each table's min/max available positions without scanning it.
