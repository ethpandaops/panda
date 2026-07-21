---
name: Use the Devnet Issue Contract
description: The shared shapes every devnet investigation stage exchanges — the issue record (summary, title, classification incl. severity, first_bad, affected, evidence, co_present, fingerprint, handles, confidence), the evidence item (source, ref, at, detail), and the handles + setup_summary that make an issue reproducible. Severity ranks impact and is a different axis from confidence (evidence strength). Use when emitting or consuming an issue between watch, collation, fingerprint, root-cause, trace, review, or feedback stages.
tags: [devnet, issue, contract, schema, pipeline, evidence]
triggers:
  - what fields does a devnet issue have
  - issue record format between pipeline stages
  - evidence item shape citation format
  - where do first_bad affected handles fingerprint go in an issue
  - schema for reporting a missed slot or orphaned block as an issue
  - severity vs confidence on an issue record
---

Owns the ISSUE record and EVIDENCE item shapes exchanged by the investigation pipeline,
including the `classification.severity` impact scale. Stages reference this file instead
of re-declaring fields; identity fields (`fingerprint`) are produced by
`runbooks://devnet_issue_fingerprint_dedupe`, and `confidence` — evidence strength, a
different axis from severity — uses the scale in `runbooks://evidence_discipline`.

## Issue record

Write the free-text `summary` first — reason in prose, then fill the classified fields
from it. A filled example (values illustrative):

```yaml
issue:
  summary: >
    Checkpoints froze at epoch 12 on all nodes while head kept advancing.
    First bad artifact: epoch-12 participation dropped to 41% (completed epoch,
    Dora + direct attestation counts agree). vc-2 and vc-3 (teku) were restarting
    from epoch 11 onward — offline stake ≈ 45%, above the >1/3 stall threshold
    (`runbooks://ethereum_protocol_model`), explains the loss. Chronic buildoor
    reveal errors predate the stall and are recorded as a co-present signal.
  title: "Finality stalls at epoch 12 while head advances"
  classification:
    category: finality-stall     # failure MODE, not suspected owner — enum under Field rules
    layer: consensus             # consensus|execution|network|tooling|cross-layer
    spread: subset               # single-client|subset|all-clients|network-wide|unknown
    severity: major              # impact rank critical|major|minor — NOT confidence; criteria under Field rules
  first_bad:
    kind: epoch                  # slot|block|epoch|log|rpc|test|service-state
    value: "12"
    at: "2026-07-01T10:42:07Z"
  affected:
    - { node: "vc-2-teku", role: vc, client: teku, image: "consensys/teku:26.1.0" }
    - { node: "vc-3-teku", role: vc, client: teku, image: "consensys/teku:26.1.0" }
  evidence:
    - source: dora
      ref: "GET {dora}/api/v1/epoch/12 — {dora} from kurtosis port print devnet-1 dora http"
      at: "epoch 12"
      detail: "participation 41.2% on completed epoch 12 (matches beacon-api attestation counts)"
    - source: beacon-api
      ref: "GET /eth/v1/beacon/states/head/finality_checkpoints on each CL, two reads 2 slots apart"
      at: "slots 415-417"
      detail: "head advanced 415→417 on every CL; finalized checkpoint pinned at epoch 12"
    - source: kurtosis
      ref: "kurtosis service logs devnet-1 vc-2-teku -n 3000"
      at: "2026-07-01T10:41:55Z"
      detail: "restart loop from 10:41:55Z; vc-2+vc-3 hold ≈45% of keys per validator-ranges"
  co_present: ["chronic buildoor reveal 400s since genesis (separate signal)"]
  fingerprint:                   # full block owned by runbooks://devnet_issue_fingerprint_dedupe — reasoning first
    rationale: "no prior issue shares first artifact or component signature"
    key: "v1:finality-stall:consensus:gloas:head-advances-finality-stalled:subset:vc:teku:participation-drop"
    decision: new                # new|duplicate|variant|insufficient-context
    matched: ""
    variant_dimension: ""        # filled when decision is variant
    confidence: high             # fingerprint-local; scale: runbooks://evidence_discipline
  handles:
    { snapshot_id: "snap-9", sandbox_id: "sbx-4", enclave: "devnet-1", network: "",
      setup_summary: { fork_schedule: { gloas: 8 }, blob_schedule: {}, load: [], builders: ["buildoor"] } }
  confidence: high               # scale: runbooks://evidence_discipline
```

Field rules:

- `category` names the failure MODE (`consensus-split`, `finality-stall`,
  `invalid-block`, `payload-absent`, `builder-path-degraded`, `execution-mismatch`,
  `client-crash`, `missed-proposals`, `lifecycle-stuck`, `performance`, `tooling`,
  `unknown`); `spread` is extent, not blame.
- `classification.severity` ranks IMPACT — a different axis from `confidence` (evidence
  strength) and from blame: `critical` — a network-wide liveness/safety failure,
  sustained/unrecovering finality loss, or data loss, the network not usably progressing
  for its purpose; `major` — a real defect degrading one client or a subset, or chain
  quality — including bounded or recovering finality lag — while the chain still
  progresses; `minor` — isolated, self-healing, or within-expected behavior with no
  chain-level impact. A within-expected observation is `minor`, not omitted. Severity may
  be re-judged as investigation sharpens impact (e.g. `runbooks://devnet_issue_root_cause`).
  Consumers that rank a board apply their own duration/degree thresholds (e.g. epochs
  stalled) over this scale (`runbooks://devnet_bug_report`).
- `first_bad` anchors the issue at the earliest artifact that explains later symptoms;
  `value`/`at` MUST be a concrete coordinate (a slot, block, epoch, or log timestamp),
  never prose — downstream stages restore to just before it
  (`runbooks://devnet_issue_root_cause`) and cap confidence on a prose-only `first_bad`
  (`runbooks://devnet_issue_reachability_trace`). For a gradual or monotonic onset, set
  `value`/`at` to the best onset coordinate (e.g. the first threshold-crossing epoch)
  and put the gradient itself in `summary` and an evidence `detail`.
- `affected` identifies components by role + client + image — a node name alone is
  evidence, not identity. When a dimension cannot be resolved without guessing
  (public devnets often expose no validator→node mapping), keep what is known and
  set the rest to `unknown`; the gap becomes a feedback task
  (`runbooks://devnet_issue_feedback_queue`), never an invented value.
- `handles` carries whatever makes the issue reproducible: ids (snapshot, sandbox,
  enclave, or public network) plus the `setup_summary` (fork/blob schedule, load,
  builders) that reproduction and reachability judge against. An issue with no handle
  gets a `snapshot` feedback task (`runbooks://devnet_issue_feedback_queue`).
  Fill only the id kind that exists — a public-network issue carries `network` with the
  other ids empty. Inside `setup_summary`, fork/blob schedule are required; `load`
  and `builders` may be empty when the grounded context does not establish them —
  empty means "not established", not "none configured".

## Evidence item

Every concrete claim, in any stage output, is one of these:

```yaml
- source: kurtosis        # tool or datasource that produced it
  ref: "kurtosis service logs devnet-1 vc-2-teku -n 3000"   # re-derives it when re-run
  at: "2026-07-01T10:41:55Z"   # slot, block, epoch, or timestamp — whichever anchors it
  detail: "restart loop: 6 'Shutting down' lines in 90s"    # verbatim values preserved
```

`ref` is executable and self-contained — a reader can re-run it exactly as written,
never "same query as above" (`runbooks://evidence_discipline`). A placeholder like
`{dora}` is allowed only when the same `ref` also says how to resolve it, as in the
issue example above.
