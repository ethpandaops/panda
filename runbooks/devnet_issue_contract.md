---
name: Devnet Issue Contract
description: The shared shapes every devnet investigation stage exchanges — the issue record, the evidence item, and the handles that make an issue reproducible. Use when emitting or consuming an issue between watch, collation, fingerprint, root-cause, trace, review, or feedback stages.
tags: [devnet, issue, contract, schema, pipeline, evidence]
triggers:
  - what fields does a devnet issue have
  - issue record format between pipeline stages
  - evidence item shape citation format
---

Owns the ISSUE record and EVIDENCE item shapes exchanged by the investigation pipeline.
Stages reference this file instead of re-declaring fields; identity fields
(`fingerprint`) are produced by `runbooks://devnet_issue_fingerprint_dedupe`, and
`confidence` uses the scale in `runbooks://evidence_discipline`.

## Issue record

Write the free-text `summary` first — reason in prose, then fill the classified fields
from it. A filled example (values illustrative):

```yaml
issue:
  title: "Finality stalls at epoch 12 while head advances"
  summary: >
    Checkpoints froze at epoch 12 on all nodes while head kept advancing.
    First bad artifact: epoch-12 participation dropped to 41% (completed epoch,
    Dora + direct attestation counts agree). vc-2 and vc-3 (teku) were restarting
    from epoch 11 onward — offline stake ≈ 45% explains the loss. Chronic buildoor
    reveal errors predate the stall and are recorded as a co-present signal.
  classification:
    category: finality-stall     # failure MODE, not suspected owner
    layer: consensus             # consensus|execution|network|tooling|cross-layer
    spread: subset               # single-client|subset|all-clients|network-wide|unknown
  first_bad:
    kind: epoch                  # slot|block|epoch|log|rpc|test|service-state
    value: "12"
    at: "2026-07-01T10:42:07Z"
  affected:
    - { node: "vc-2-teku", role: vc, client: teku, image: "consensys/teku:26.1.0" }
    - { node: "vc-3-teku", role: vc, client: teku, image: "consensys/teku:26.1.0" }
  evidence:
    - source: dora
      ref: "GET /api/v1/epoch/12"
      detail: "participation 41.2% on completed epoch 12"
    - source: kurtosis
      ref: "kurtosis service logs devnet-1 vc-2-teku -n 3000"
      detail: "restart loop from 10:41:55Z"
  co_present: ["chronic buildoor reveal 400s since genesis (separate signal)"]
  fingerprint:
    key: "v1:finality-stall:consensus:gloas:head-advances-finality-stalled:subset:vc:teku:participation-drop"
    decision: new                # new|duplicate|variant|insufficient-context
    matched: ""
    rationale: "no prior issue shares first artifact or component signature"
  handles: { snapshot_id: "snap-9", sandbox_id: "sbx-4", enclave: "devnet-1", network: "" }
  confidence: high               # scale: runbooks://evidence_discipline
```

Field rules:

- `category` names the failure MODE (`consensus-split`, `finality-stall`,
  `invalid-block`, `payload-absent`, `builder-path-degraded`, `execution-mismatch`,
  `client-crash`, `missed-proposals`, `lifecycle-stuck`, `performance`, `tooling`,
  `unknown`); `spread` is extent, not blame.
- `first_bad` anchors the issue at the earliest artifact that explains later symptoms.
- `affected` identifies components by role + client + image — a node name alone is
  evidence, not identity.
- `handles` carries whatever makes the issue reproducible (snapshot, sandbox, enclave,
  or hosted network id); an issue with no handle gets a `snapshot` feedback task
  (`runbooks://devnet_issue_feedback_queue`).

## Evidence item

Every concrete claim, in any stage output, is one of these:

```yaml
- source: ""     # tool or datasource that produced it
  ref: ""        # the command / query / endpoint that RE-DERIVES it
  at: ""         # slot, block, epoch, or timestamp — whichever anchors it
  detail: ""     # what it shows, verbatim values preserved
```

`ref` is executable — a reader can re-run it (`runbooks://evidence_discipline`).
