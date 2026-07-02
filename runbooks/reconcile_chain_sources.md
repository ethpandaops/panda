---
name: Reconcile Disagreeing Chain Sources
description: Resolve disagreements between chain data sources — Dora/explorer, direct beacon and EL RPC, indexed ClickHouse (raw events, refined _head, canonical _canonical), OTel logs, and Prometheus metrics — about slot status, head roots, node liveness, or participation. Owns the source-authority matrix (what each view is evidence FOR and its blind spots) and the verification order. Use when an indexer shows a node offline but its RPC responds, participation numbers differ between sources, or a slot exists in one view and not another.
tags: [datasources, reconciliation, dora, beacon-api, clickhouse, evidence]
triggers:
  - dora and the beacon node disagree
  - two datasources disagree which one to trust
  - indexer shows node offline but rpc responds
  - participation numbers differ between explorer and clickhouse
  - node looks down in one tool but healthy in another
---

Owns the source-authority matrix for chain views and the reconciliation procedure.
The principle — report the disagreement and what each source is evidence FOR, never
silently pick one — is owned by `runbooks://evidence_discipline`; authority for hosted
*metadata* (inventory, fork schedule, endpoints) is owned by
`runbooks://hosted_devnet_context`.

## Inputs
Required: the disagreeing claims, each with its source.
Preferred: the network/enclave, the slot/epoch window, and direct RPC access to at
least one node.

## Output
The reconciled fact with the disagreement recorded (each side as an evidence item with
what it was evidence for), or an explicit unresolved disagreement naming the third
source that would settle it. Example:

```yaml
reconciliation:
  summary: >
    Dora showed cl-3 "offline" while its RPC answered: cl-3 is on a minority fork —
    its head root differs from the canonical-fork nodes at slot 4711. Indexer view
    and RPC view were both correct for what they measure.
  resolved: true
  blind_spot: "explorer indexes the canonical fork only"
  sides:
    - { source: dora, ref: "GET /api/v1/validator/…", at: "epoch 147", detail: "cl-3 listed offline" }
    - { source: beacon-api, ref: "GET /eth/v1/beacon/headers/head on cl-3", at: "slot 4711", detail: "head root 0xcd…, healthy sync" }
  settling_query: "compare head roots + finality checkpoints across all CLs"
```

## Source authority

| Source | Authoritative for | Blind spots |
| --- | --- | --- |
| Direct beacon / EL RPC (one node) | that node's own live view: head, finality checkpoints, sync, peers | one node ≠ the network; no history; a minority-fork node reports its fork confidently |
| Dora / explorer | canonical-chain history: slot status, epoch summaries, duties, participation | reflects the canonical fork only — minority-fork nodes look "offline"; indexing lags the head |
| ClickHouse raw (xatu events) | how many / how often / how late / from whom: gossip timing, per-peer and per-observer counts | observer coverage limits; events ≠ canonical truth (`runbooks://clickhouse_querying`) |
| ClickHouse refined `_head` | live one-value-per-slot state for real-time monitoring | may reorg — not finalized truth; CBT coverage gaps read as missing data (`runbooks://clickhouse_querying`) |
| ClickHouse `_canonical` | finalized chain-state history, no reorgs | lags finality; hides orphans/reorgs by design (`runbooks://clickhouse_querying`) |
| OTel logs | what a service said, verbatim, with timestamps | absence means "not in the fetched window or not shipping", never "the service is down" (`runbooks://kurtosis_devnet`) |
| Prometheus metrics | time-series liveness and resource trends | scrape gaps mimic downtime (`runbooks://prometheus_devnet_health`) |

## Procedure

1. **Restate each claim as an evidence item** — source, the exact ref that re-derives
   it, and what it anchors to (`runbooks://devnet_issue_contract`). Most
   "disagreements" dissolve here: the two refs answer different questions.
2. **Classify the axis of disagreement:** per-node view vs canonical history, raw
   events vs deduplicated truth, `_head` vs `_canonical`, staleness/lag, or coverage
   (observer, scrape, log shipping, CBT transformation).
3. **Verify against a third source of a different kind** — a second indexer inherits
   the first's blind spot; a direct RPC read settles what an indexer and a log
   disagree about (`runbooks://evidence_discipline`).
4. **Report the disagreement with the resolution**, and copy any data-quality caveat
   into downstream reports.

## Common cases

| Disagreement | Usual cause | Settling query |
| --- | --- | --- |
| Explorer shows node offline, its RPC responds | minority fork (indexers track the canonical fork only) or indexing lag | compare head root + finalized checkpoint from that node's RPC against a canonical-fork node |
| Participation differs between sources | one side read the in-progress head epoch — judge completed epochs only; a "finalized" epoch showing <66.7% participation means distrust that source (both rules: `runbooks://ethereum_protocol_model`) | re-read both for the last completed epoch; verify against checkpoints |
| Slot present in raw events, absent in refined | block was orphaned/reorged out — canonical views drop it; or the CBT pipeline has not processed that range yet (`runbooks://clickhouse_querying`) | query the raw table for the block root; check transformation coverage |
| Logs silent, metrics healthy (or inverse) | log-shipping or scrape gap, not service state (gap semantics: `runbooks://prometheus_devnet_health`) | hit the service API directly |
| Two nodes report different heads | that is a finding, not a data-quality issue — a split | switch to `runbooks://debug_ethereum_network` (network split branch) |

## Self-Check

Before returning:
- Every side of the disagreement is an evidence item with a re-derivable ref.
- The resolution names which blind spot explained the mismatch.
- No source was silently dropped; unresolved disagreements name the third source
  that would settle them.
