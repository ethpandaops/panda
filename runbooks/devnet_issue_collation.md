---
name: Collate Devnet Watch Issues
description: Turn neutral watch observations from a devnet into self-contained issue records with evidence, scope, affected components, and a restore handle — deciding what is actually an issue and packaging each one for downstream investigation. Use after a watch step, before root-cause work.
tags: [devnet, issue, collation, triage, judgment, pipeline]
triggers:
  - turn watch observations into issue records
  - decide whether devnet observations are real problems
  - collate and group duplicate watch observations into packaged issues
---

Owns the judgment step: consumes the Output of `runbooks://devnet_watch` and emits
issue records in the shape of `runbooks://devnet_issue_contract`. Identity fields come
from `runbooks://devnet_issue_fingerprint_dedupe`; terminal issues go to
`runbooks://devnet_issue_root_cause`, gaps to `runbooks://devnet_issue_feedback_queue`.

## Inputs
Required: the watch output (observations, setup summary, window, handles with the
network target and any final snapshot id).
Preferred: sandbox id + enclave (for narrow re-verification), the source devnet id, and
prior issue records for dedupe.
Input observations are FACTS, not verdicts. If the setup summary is missing,
reconstruct it before judging — without it, normal idle behavior (empty blocks on an
unloaded network) gets over-reported as issues.

## Output
A list of issue records (`runbooks://devnet_issue_contract`) — empty when the window is
healthy, with a one-paragraph healthy-window summary in its place. Root cause and final
blame belong to `runbooks://devnet_issue_root_cause`; the emitted issues ARE the
handoff — an orchestrator feeds each one to that runbook, there is no separate
handoff field. Shape (values illustrative):

```yaml
collation:
  healthy_window_summary: ""     # one paragraph INSTEAD of issues when nothing is wrong;
                                 # when issues ARE emitted (e.g. builder-path-degraded on a
                                 # finalizing chain), the healthy-chain verdict lives in each
                                 # issue's summary/evidence, not here
  issues:
    - summary: >
        Checkpoints froze at epoch 12 while head advanced; teku VCs vc-2/vc-3
        restart-looping from epoch 11 — offline stake explains the participation loss.
      title: "Finality stalls at epoch 12 while head advances"
      # TRUNCATED example: emit the FULL issue record — also classification,
      # first_bad, affected, evidence, co_present, confidence — copied from the
      # example in runbooks://devnet_issue_contract
      fingerprint: { decision: new }   # identity via runbooks://devnet_issue_fingerprint_dedupe
      handles:                         # watch handles mapped in: final_snapshot_id -> snapshot_id,
                                       # network_target flattened (sandbox_id/enclave copied, network_id -> network),
                                       # watch setup_summary copied WHOLE into handles.setup_summary
        { snapshot_id: "snap-9", sandbox_id: "sbx-4", enclave: "devnet-1", network: "",
          setup_summary: { fork_schedule: { gloas: 8 }, blob_schedule: {}, load: [], builders: ["buildoor"] } }
  feedback: []                   # gap tasks (runbooks://devnet_issue_feedback_queue),
                                 # emitted here beside issues — not embedded inside them
```

## Correlation rules

1. **One timeline.** Align logs, CL slots/epochs, EL blocks, and tooling events on the
   sandbox clock.
2. **Group duplicates.** The same normalized error across many nodes at the same time
   is usually ONE issue with multiple affected components — grouping recipe:
   `runbooks://devnet_issue_fingerprint_dedupe`.
3. **Disagreement is neutral.** Record client positions on head/payload/validity;
   a majority can share the bug (`runbooks://evidence_discipline`).
4. **Judge against setup.** Empty blocks, idle mempools, absent blob/builder traffic
   are issues only under configured demand.
5. **First cause over symptom.** Anchor `first_bad` at the earliest event that explains
   later symptoms; post-split/post-stall errors are usually consequences.
6. **Separate co-present distractors.** A chronic error that predates and outlives a
   bounded outage goes in `co_present`, with its timing, not into the root narrative.

## Fork-aware judgment — triage gate

Apply `runbooks://ethereum_protocol_model` — record the block `version` and whether a
symptom starts exactly at a fork/BPO boundary or only after a later safe/finalized-head
change. This triage is a **gate, not advice**: an issue MUST NOT be framed as a client
bug — neither in its `summary` nor by choosing a `classification.category` to imply
one (`category` names the failure mode, never a suspected owner —
`runbooks://devnet_issue_contract`) — until the row below that matches its symptom has
been completed and the cheaper protocol-level explanation ruled out. Most "client bug"
collations are participation or builder-path effects seen from one node.

| Symptom | Required triage BEFORE classing it | Thresholds (owner) |
| --- | --- | --- |
| **Finality stall** | service status → offline-stake fraction → completed-epoch participation, cheapest first | >1/3 offline stake stalls finality on its own → class **lifecycle/participation**, consensus as affected layer, not a client bug (`runbooks://ethereum_protocol_model`) |
| **Missed slots on Gloas/ePBS** | distinguish a missed beacon block from a canonical block with a **missing payload**; confirm absence via the next slot's PTC `payload_attestations` (slot S's verdict rides in block S+1) before framing as missed-proposals | PTC is the authoritative payload-presence verdict (`runbooks://ethereum_protocol_model`) |
| **Absent payloads on one `builder_index`** (incl. the self-build sentinel) | map `builder_index` through builder evidence before blaming a client | one `builder_index` concentrating absent payloads is a builder reveal/registration problem until proven otherwise; a few missing payloads do NOT by themselves explain a finality stall (`runbooks://ethereum_protocol_model`) |
| **Fulu/PeerDAS DA warnings** | include sidecar/data-column availability; check whether DA warnings precede reveal or validation failures | on ePBS, PTC `blob_data_available` is the DA verdict (`runbooks://ethereum_protocol_model`) |

If head and finality advance while builder/VC services repeatedly fail to
produce/reveal/register/bid, emit `builder-path-degraded` alongside — not instead
of — the healthy-chain verdict. Anchor final/safe-block-unavailable errors only
after checking finality progression; they are usually downstream of a stall.

A `converged` or coverage flag arriving with upstream scan/watch output is an input
**signal, not a verdict**: never promote it into a single settled root cause in an
issue or the summary. Final blame is assigned downstream by
`runbooks://devnet_issue_root_cause`, not here.

## Verification

For a borderline issue and a still-reachable target, run ONE narrow query — re-check
head/finality on a node or two, re-pull a bounded log window around `first_bad`, verify
a service image, or inspect the named block/payload/tx. Keep it to that one query; if
evidence still only narrows the class, say so and name the next query for the
downstream stage.

When an issue is real but missing a handoff field, shape the gap with
`runbooks://devnet_issue_feedback_queue` (a `snapshot` task for a missing broken-state
handle, `watch` for a too-short window, `investigate` for one missing query) and emit
it in the top-level `feedback` list instead of hiding it in prose.

## Self-Check

Before returning:
- Every issue follows `runbooks://devnet_issue_contract`, fingerprint included.
- Shared symptoms are grouped by first artifact and component roles, not service suffix.
- Empty blocks, missing blobs, and absent builder activity were judged against
  configured demand.
- Every issue carries a handle or a `snapshot` feedback task.
- No client-bug or converged-root-cause framing was emitted before its fork-triage
  row (finality / ePBS payload / builder / DA) was completed.
- Evidence stays factual; no final root cause is assigned.
