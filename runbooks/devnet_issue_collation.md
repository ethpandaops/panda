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
blame belong to `runbooks://devnet_issue_root_cause`. Shape (values illustrative):

```yaml
collation:
  healthy_window_summary: ""     # one paragraph INSTEAD of issues when nothing is wrong
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
                                       # watch setup_summary copied WHOLE into handles.setup_summary
        { snapshot_id: "snap-9", sandbox_id: "sbx-4", enclave: "devnet-1", network: "",
          setup_summary: { fork_schedule: { gloas: 8 }, blob_schedule: {}, load: [], builders: ["buildoor"] } }
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

## Fork-aware judgment

Apply `runbooks://ethereum_protocol_model` — record the block `version` and whether a
symptom starts exactly at a fork/BPO boundary or only after a later safe/finalized-head
change. The load-bearing cases:

- **Gloas/ePBS:** distinguish missed beacon blocks from canonical blocks with missing
  payloads; confirm a missing payload via next-slot PTC `payload_attestations`; map
  `builder_index` (and the self-build sentinel) through builder evidence — slot states,
  PTC, and `builder_index` semantics owned by `runbooks://ethereum_protocol_model`. If head and
  finality advance while builder/VC services repeatedly fail to
  produce/reveal/register/bid, emit `builder-path-degraded` alongside — not instead
  of — the healthy-chain verdict. Anchor final/safe-block-unavailable errors only
  after checking finality progression; they are often downstream of a stall.
- **Finality stalls:** run the stall triage in `runbooks://ethereum_protocol_model`
  (service status → offline stake fraction → completed-epoch participation) before any
  client-bug framing; >1/3 offline stake classifies as lifecycle/participation with
  consensus as the affected layer.
- **Fulu/PeerDAS:** include sidecar/data-column availability in payload and DA issues;
  check whether DA warnings precede reveal or validation failures.

## Verification

For a borderline issue and a still-live enclave, run ONE narrow query — re-check
head/finality on a node or two, re-pull a bounded log window around `first_bad`, verify
a service image, or inspect the named block/payload/tx. Keep it to that one query; if
evidence still only narrows the class, say so and name the next query for the
downstream stage.

When an issue is real but missing a handoff field, shape the gap with
`runbooks://devnet_issue_feedback_queue` (a `snapshot` task for a missing broken-state
handle, `watch` for a too-short window, `investigate` for one missing query) instead of
hiding it in prose.

## Self-Check

Before returning:
- Every issue follows `runbooks://devnet_issue_contract`, fingerprint included.
- Shared symptoms are grouped by first artifact and component roles, not service suffix.
- Empty blocks, missing blobs, and absent builder activity were judged against
  configured demand.
- Every issue carries a handle or a `snapshot` feedback task.
- Evidence stays factual; no final root cause is assigned.
