---
name: Diagnose Slow ClickHouse Query
description: Troubleshoot and optimize slow-running ClickHouse queries
tags: [clickhouse, performance, query, optimization, debugging]
prerequisites: [xatu, xatu-cbt]
---

When a query runs slowly or times out, you MUST understand the query execution plan before attempting optimization.

## Approach

1. **Get query execution stats** - You MUST first understand what the query is doing. Use EXPLAIN to analyze.

   ```python
   from ethpandaops import clickhouse

   slow_query = """
   YOUR SLOW QUERY HERE
   """

   # Get execution plan
   explain = clickhouse.query("xatu", f"EXPLAIN {slow_query}")
   print(explain)
   ```

2. **Check the target cluster** - Queries against `xatu` (raw events) are significantly slower than `xatu-cbt` (pre-aggregated). You SHOULD prefer xatu-cbt when possible.

   - `xatu`: Raw event data, large tables, use for detailed analysis
   - `xatu-cbt`: Pre-aggregated data, faster queries, use for common metrics

3. **Verify time range** - You MUST use indexed date columns for filtering. Large unbounded queries will be slow.

   Common patterns:
   - Use `slot_start_date_time >= now() - INTERVAL X HOUR` instead of scanning all data
   - Use partitioned date columns (`slot_start_date_time`, `wallclock_date_time`)
   - Avoid `toDate(timestamp)` - use native date columns

4. **Check cardinality** - High-cardinality GROUP BY clauses cause memory pressure. You SHOULD limit result sets.

   ```sql
   -- Bad: Returns potentially millions of rows
   SELECT validator_index, count(*) FROM attestations GROUP BY validator_index

   -- Better: Limit output
   SELECT validator_index, count(*) as cnt
   FROM attestations
   GROUP BY validator_index
   ORDER BY cnt DESC
   LIMIT 100
   ```

5. **Optimize JOIN order** - You MUST put the smaller table on the right side of JOINs. ClickHouse reads the right table into memory.

6. **Use materialized views** - For repeated queries, you MAY suggest creating a materialized view if the query pattern is common.

7. **Check for missing indexes** - Use `search(type="examples", query="clickhouse schema")` to understand available indexes.

## Common Performance Issues

| Issue | Symptom | Solution |
|-------|---------|----------|
| No date filter | Query scans entire table | Add `WHERE slot_start_date_time >= ...` |
| Wrong cluster | Slow aggregations | Use xatu-cbt for pre-aggregated data |
| Large result set | Memory exceeded | Add LIMIT or more specific WHERE |
| Bad JOIN order | High memory usage | Smaller table on right side of JOIN |
| String comparison | Slow WHERE clauses | Use numeric IDs when available |

## Query Optimization Checklist

- [ ] Using correct cluster (xatu vs xatu-cbt)?
- [ ] Time range filter present and uses indexed column?
- [ ] LIMIT clause to prevent huge result sets?
- [ ] GROUP BY cardinality reasonable?
- [ ] JOINs ordered correctly (small table on right)?
- [ ] Using numeric columns instead of strings where possible?

## Example: Optimizing a Slow Query

```sql
-- Slow: Scans all data, high cardinality GROUP BY
SELECT
    meta_client_name,
    count(*)
FROM beacon_api_eth_v1_events_block
GROUP BY meta_client_name

-- Fast: Time bounded, using xatu-cbt
SELECT
    meta_client_name,
    count(*) as block_count
FROM mainnet.fct_block_first_seen_by_node
WHERE slot_start_date_time >= now() - INTERVAL 24 HOUR
GROUP BY meta_client_name
ORDER BY block_count DESC
```

## Notes

- Query timeout is typically 30 seconds - design queries to complete faster
- Memory limit per query may cause OOM errors on large aggregations
- Consider breaking large analyses into smaller time windows
- Use `search(type="examples", query="...")` to find optimized query patterns for common analyses
