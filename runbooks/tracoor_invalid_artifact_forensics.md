---
name: Investigate Invalid Artifacts with Tracoor
description: Hunt the artifacts nodes rejected — invalid beacon blocks, bad blob sidecars, and execution bad blocks (debug_getBadBlocks) captured by Tracoor — for an incident window, map each rejection to the client that refused it, and turn them into first_bad anchors and evidence for a devnet investigation. Use when a consensus incident needs the rejected-artifact evidence trail — who gossiped an invalid block, which client rejected it, which builder produced a bad execution block.
tags: [tracoor, forensics, invalid-block, bad-blobs, consensus, evidence]
triggers:
  - invalid beacon block which client rejected it
  - bad blob sidecar forensics for an incident window
  - execution bad blocks debug_getBadBlocks who built it
  - tracoor rejected artifact evidence
  - find the first invalid block behind a split or stall
prerequisites: [tracoor]
---

Owns the INVALID-ARTIFACT evidence trail: enumerating what nodes rejected, when, and
by which client — the primary forensic input when a split, stall, or validation
failure needs its `first_bad` artifact. The `tracoor` library owns the mechanics
(`list_beacon_bad_blocks`, `list_beacon_bad_blobs`, `list_execution_bad_blocks`, and
their `count_*` twins, all window-filterable — search the examples index for "tracoor
bad block" for exact call patterns). Interpreting what a rejection means under the
active fork belongs to `runbooks://ethereum_protocol_model`; code-level blame to
`runbooks://ethereum_spec_source_drilldown`.

## Inputs
Required: the network and an incident window (slots, epochs, or timestamps).
Preferred: the suspected artifact kind (beacon block / blob / execution block) and the
implicated client or block root from an existing diagnosis.

## Output
The rejected-artifact inventory for the window — each artifact an evidence item with
its rejecting client — plus the earliest artifact that explains later symptoms,
suitable as the issue record's `first_bad` (`runbooks://devnet_issue_contract`).
Handoff shape (values illustrative):

```yaml
first_bad: { kind: block, value: "0xab12…", at: "slot 4711 / 2026-07-01T10:42:07Z" }   # kind: slot|block|epoch|log|rpc|test|service-state
evidence:
  - { source: tracoor, ref: "tracoor.list_beacon_bad_blocks('<network>', after='2026-07-01T10:00Z', before='2026-07-01T11:00Z')", at: "slot 4711", detail: "rejected by lighthouse (beacon_implementation), root 0xab12…" }
```

## Procedure

Python is a shape to adapt — substitute network and window.

1. **Count before listing.** Establish the scale of EVERY artifact kind in the
   window first — zero in one kind says nothing about the others:

   ```python
   from ethpandaops import tracoor
   w = dict(after="<iso>", before="<iso>")
   n_blocks = tracoor.count_beacon_bad_blocks("<network>", **w)
   n_blobs  = tracoor.count_beacon_bad_blobs("<network>", **w)
   n_exec   = tracoor.count_execution_bad_blocks("<network>", **w)
   ```

   Zero rejected artifacts is a finding: the incident evidence lies elsewhere
   (missed production, network partition) — return to
   `runbooks://debug_ethereum_network`'s branch table.

2. **List each artifact kind in the window** — beacon bad blocks, bad blobs (by
   `block_root` + `index`), execution bad blocks. Record per artifact: slot or block
   number, root/hash, the rejecting node, and the rejecting implementation
   (`beacon_implementation` / `execution_implementation`).

3. **Map rejections to clients.** Group by implementation: one client family
   rejecting what others accept points at that family's validation path (or everyone
   else's) — the minority can be the only correct one
   (`runbooks://evidence_discipline`). Execution bad blocks carry
   `block_extra_data`, which often identifies the builder that produced them.

4. **Anchor first_bad.** Sort beacon artifacts by slot/time and execution bad blocks
   by `block_number`/`fetched_at` (they expose no consensus slot); the earliest
   rejected artifact that explains later symptoms is the `first_bad` candidate —
   later rejections are often descendants of the same fault. Cross-check the slot
   (or the block-number/fetched-at streak) against fork boundaries
   (`runbooks://ethereum_protocol_model`): a rejection streak starting exactly at an
   activation epoch is a fork-rules divergence, not random corruption.

5. **Hand off.** Feed the inventory into the issue record as evidence; escalate a
   client-specific rejection to `runbooks://ethereum_spec_source_drilldown` with the
   exact artifact root and the rejecting client's image.

## Rules

- **Rejected ≠ wrong.** A bad-block entry proves one node refused it, not that the
  block was invalid — a buggy validator rejects good blocks. The verdict needs the
  spec rule or cross-client agreement.
- **Absence is bounded.** Tracoor records what its connected nodes captured; an empty
  window means no capture by those nodes, not that no invalid artifact existed.
- Every listed artifact keeps its re-derivable ref (the exact tracoor call + window).

## Self-Check

Before returning:
- Counts were taken before listing, and the window is stated on every claim.
- Each artifact carries its rejecting node AND implementation.
- The first_bad candidate is the earliest explaining artifact, checked against fork
  boundaries.
- No rejection was reported as proven invalidity without a spec rule or cross-client
  agreement.
