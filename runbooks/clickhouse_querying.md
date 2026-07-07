---
name: Query ClickHouse Well
description: Write ClickHouse queries that read only what they need, and fix slow ones — EXPLAIN first, pick clickhouse-raw vs clickhouse-refined, address the network's database (public networks in default with meta_network_name, devnets in their own <network> database on clickhouse-raw, refined via the <network>. prefix), filter on the partition key (slot_start_date_time, or block_number via the fct_block_head bridge for trace tables like int_transaction_call_frame), choose raw event data vs canonical deduplicated views, and bound the result. Use before any non-trivial query and whenever a query is slow, scans too much, or times out.
tags: [clickhouse, performance, partition, raw, canonical, block-number]
triggers:
  - clickhouse query slow or timing out
  - which cluster clickhouse raw or refined
  - query execution traces call frames opcode gas by time window
  - propagation timing duplicate gossip per peer counts
  - orphaned or reorged blocks missing from results
  - full table scan memory limit exceeded
  - which database has devnet data in raw clickhouse
prerequisites: [clickhouse-raw, clickhouse-refined]
---

Owns how to query ClickHouse: cluster choice, partition discipline, raw vs canonical
data, and block-number-partitioned tables. Reference before running a non-trivial query
and when one is slow.

## Inputs
Required: a query, or the goal of a query, and the target datasource.
Preferred: the table(s) involved and the time or block window.

## Output
A query that reads only what it needs — or, for a slow query, the specific change that
fixes it and why.

## Running a query

Use the query surface that matches your environment — never reach ClickHouse directly
with `clickhouse-client`, `curl`, or credentials from the environment.

- **Terminal / CLI worker** — run through the `panda` CLI:

  ```
  panda clickhouse query <datasource> "<sql>"
  ```

  `EXPLAIN <query>` runs the same way. Look up a table's columns rather than guessing:
  `panda schema <datasource> <database> <table>` for a `default`/public-network table,
  but it cannot address a hyphenated devnet database (the name fails identifier
  validation and the listing shows zero tables) — for those, describe the table with a
  query instead: ``panda clickhouse query clickhouse-raw "DESCRIBE TABLE `<network>`.<table>"``
  (and ``SHOW TABLES FROM `<network>``` to list them).

- **Python sandbox (`execute_python`)** — use the `ethpandaops` library:

  ```python
  from ethpandaops import clickhouse
  df = clickhouse.query("<datasource>", "<sql>")
  ```

Both take the same `<datasource>` — the datasource that holds the dataset's placement
(list them with `panda datasets`). Addressing by cluster:

- **Refined (`clickhouse-refined`)** — always the `<network>.` table prefix.
- **Raw (`clickhouse-raw`)** — public networks (mainnet, testnets) live in the
  `default` database; each devnet has its own database named after the network,
  e.g. ``FROM `glamsterdam-devnet-6`.beacon_api_eth_v1_events_block``. In both
  layouts keep `meta_network_name = '<network>'` plus a `slot_start_date_time`
  bound in the WHERE — the primary key leads with them, and the cluster rejects
  queries that use neither (`force_primary_key`). An empty result from the wrong
  database looks like missing data — `SHOW DATABASES` settles where the network
  lives.

## Core procedure

1. **EXPLAIN first.** Run `EXPLAIN <query>` and read the plan before changing anything.
2. **Pick the cluster.** Prefer `clickhouse-refined` (pre-aggregated, fast) for metrics;
   use `clickhouse-raw` only when the question needs event-level detail (large, slow).
   Not every network is on every cluster — if the refined `<network>` database is
   absent, the devnet is raw-only: query its `<network>` database on `clickhouse-raw`.
3. **Filter on the partition key.** Use native date columns (`slot_start_date_time`,
   `wallclock_slot_start_date_time`) bare — wrapping them in functions like
   `toDate(...)` defeats the partition index. Address the network's database and keep
   the `meta_network_name` filter per "Running a query" above.
4. **Bound the result.** Add `ORDER BY … LIMIT N`; cap high-cardinality `GROUP BY`
   (e.g. grouping by validator index).
5. **Order JOINs** with the smaller table on the RIGHT — ClickHouse loads the right
   side into memory.
6. **Prefer numeric keys** over string comparisons in `WHERE`.
7. **Use `FINAL`** on refined `fct_*` tables.
8. **Look it up, don't guess.** For an unknown schema or a common pattern, search
   the examples index rather than inventing column names.

There is a ~30s query timeout and a per-query memory limit; split a large analysis
into smaller windows.

## Raw vs canonical data

Using the wrong view silently drops the very rows the question is about.

- Use **raw** event-level data when the question is about *how many, how often, how
  late, or from whom*: propagation timing, duplicate/late gossip re-sends, per-peer or
  per-observer counts. Deduplicated views collapse exactly these rows.
- Use **canonical/refined** data for chain-state truth: finalized history and
  per-slot metrics. Refined tables come in two flavors — finalized (never reorgs;
  use for historical analysis) and `_head` (live/unfinalized, may reorg, and forks
  can leave multiple roots for one slot; use for real-time monitoring). For blocks
  the pair is `fct_block` (finalized chain, keeps orphaned rows with
  `status = 'canonical' | 'orphaned'`) vs `fct_block_head` — there is no
  `fct_block_canonical`. A `_head` read is not finalized truth.
- **Coverage before absence.** Refined/CBT tables contain only what the pipeline has
  processed — an empty result can mean "not yet transformed", not "didn't happen".
  Check `cbt.get_transformation_coverage(network, "{network}.<table>")` before
  concluding data is missing, and explain an unprocessed position (dependency bounds,
  gaps) with `cbt.debug_coverage(network, id, position)`. A 404 from the coverage
  calls means coverage is unavailable — the network may be unregistered with CBT, or
  listed by `cbt networks` yet not serving a coverage API (common for raw-only
  devnets). Either way, record coverage as unavailable, not empty, and verify with a
  bounded raw-table probe instead.
- **Orphans and reorgs:** deduplicated canonical views hide them. For stale parents,
  reorgs, or orphan rate, use a table that keeps orphaned rows — `fct_block` retains
  them with `status = 'orphaned'` — not a canonical-only view.
- When unsure whether a table is deduplicated, check its grain: one row per slot is
  canonical; many rows per (slot, node/peer) is raw.

Illustrative cases:
- "Data columns seen >5 min late, and from how many distinct peers" → raw libp2p
  gossip; a deduplicated view lands near zero.
- "How often do proposers build on a stale parent, and what happens to those blocks" →
  include orphaned blocks, not a canonical-only view.

## Block-number-partitioned tables

A table with `block_number` but no `slot_start_date_time` is partitioned by block
number — filtering it by time or `updated_date_time` scans every partition. Execution
trace tables are the common case: `int_transaction_call_frame`,
`int_transaction_call_frame_opcode_gas`, `int_transaction_call_frame_opcode_resource_gas`.

To query a time window:

1. **Resolve the range from the bridge table.** Query `<network>.fct_block_head FINAL`
   for `MIN/MAX(execution_payload_block_number)` filtered by
   `slot_start_date_time >= now() - INTERVAL <window>` (a `_head` table — fine for
   resolving a recent range, but it may reorg; not finalized truth).
2. **Pass the results as literals** into
   `WHERE block_number BETWEEN <min> AND <max>` (or `= <n>` for one block) — an
   in-query subquery bridge is rejected (`distributed_product_mode = 'deny'`). If
   the range query fails with a `force_primary_key` error, append
   `SETTINGS force_primary_key=0`: `block_number` is the partition key, so the
   pruning you need still happens. An empty
   bridge result (0/0 aggregates) is not a range — treat it as no data for the window
   and check placement/coverage rather than querying `BETWEEN 0 AND 0`.

Resolve the range from `fct_block_head` only — a `SELECT max(block_number)` subquery on
the partitioned table itself is the full scan you are avoiding, and `updated_date_time`
is not the partition key.
