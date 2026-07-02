---
name: Query ClickHouse Well
description: Write ClickHouse queries that read only what they need, and fix slow ones — EXPLAIN first, pick clickhouse-raw vs clickhouse-refined, filter on the partition key (slot_start_date_time, or block_number via the fct_block_head bridge for trace tables like int_transaction_call_frame), choose raw event data vs canonical deduplicated views, and bound the result. Use before any non-trivial query and whenever a query is slow, scans too much, or times out.
tags: [clickhouse, performance, partition, raw, canonical, block-number]
triggers:
  - clickhouse query slow or timing out
  - which cluster clickhouse raw or refined
  - query execution traces call frames opcode gas by time window
  - propagation timing duplicate gossip per peer counts
  - orphaned or reorged blocks missing from results
  - full table scan memory limit exceeded
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

## Core procedure

1. **EXPLAIN first.** Run `EXPLAIN <query>` and read the plan before changing anything.
2. **Pick the cluster.** Prefer `clickhouse-refined` (pre-aggregated, fast) for metrics;
   use `clickhouse-raw` only when the question needs event-level detail (large, slow).
3. **Filter on the partition key.** Use native date columns (`slot_start_date_time`,
   `wallclock_date_time`) bare — wrapping them in functions like `toDate(...)` defeats
   the partition index. On `clickhouse-raw` also filter
   `meta_network_name = '<network>'`; on refined use the `<network>.` table prefix.
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
- Use **canonical/refined** data for chain-state truth: finalized history,
  one-value-per-slot metrics. Refined tables come in two variants — `_canonical`
  (finalized, never reorgs; use for historical analysis) and `_head` (live, may
  reorg; use for real-time monitoring), e.g. `fct_block_canonical` vs
  `fct_block_head`. A `_head` read is not finalized truth.
- **Coverage before absence.** Refined/CBT tables contain only what the pipeline has
  processed — an empty result can mean "not yet transformed", not "didn't happen".
  Check `cbt.get_transformation_coverage(network, "{network}.<table>")` before
  concluding data is missing, and explain an unprocessed position (dependency bounds,
  gaps) with `cbt.debug_coverage(network, id, position)`.
- **Orphans and reorgs:** canonical-only tables hide them. For stale parents, reorgs,
  or orphan rate, include orphaned blocks explicitly.
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
   `WHERE block_number BETWEEN <min> AND <max>` (or `= <n>` for one block).

Resolve the range from `fct_block_head` only — a `SELECT max(block_number)` subquery on
the partitioned table itself is the full scan you are avoiding, and `updated_date_time`
is not the partition key.
