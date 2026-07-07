---
name: Trace Devnet Issue Reachability
description: Trace whether a candidate cause — a suspicious component, config, or inspected source path — could actually reach the observed devnet failure through the configured CL/EL/VC/builder/relay/load topology, and promote, weaken, or reject it. Use before any report names a component, topology, or protocol cause.
tags: [devnet, issue, reachability, trace, topology, blame]
triggers:
  - can this component actually cause the observed failure
  - verify the suspected code path is reachable at runtime
  - is the blamed client really the trigger or a victim
  - trace a bad artifact or failure back to the component that caused it
---

Owns the REACHABILITY verdict: a bounded trace from an existing candidate cause to the
observed `first_bad` artifact. It promotes, weakens, or rejects candidates already
supplied by reproduction, hypotheses, or `runbooks://ethereum_spec_source_drilldown`
(which owns commit resolution and local code-path analysis) — broad discovery stays in
`runbooks://devnet_issue_root_cause`.

## Inputs
Required: the issue record (`runbooks://devnet_issue_contract`) with `first_bad` and
affected components; the setup summary (CL/EL pairs, VCs, builders, relays, load, fork
schedule); and at least one candidate cause.
Preferred: reproduction status + recipe, hypothesis results, source findings with their
rejected paths.
With source findings, trace from the runtime entry point into the inspected code path;
without them, trace runtime reachability between components and the artifact.
When topology sources disagree (inventory vs validator ranges vs hosts observed in
logs), record the disagreement per `runbooks://reconcile_chain_sources`, trace against
the union, and note which source each actor in the chain came from.

## Output

Reasoning first, verdict second:

```yaml
trace:
  summary: >
    The candidate (buildoor reveal handler) receives the bid event and emits reveal
    400s, but the PTC verdict path that produced the missing-payload artifact is fed
    by the CL, and both sides of the CL→builder edge show the same payload id —
    reachable.
  verdict: reachable          # reachable|partially-reachable|not-reachable|insufficient-evidence
  scope: { client_specific: true, pair_specific: false, fork_specific: true, topology_dependent: false, network_wide: false }
  paths:
    - candidate: "buildoor reveal handler"
      entry_artifact: { kind: log, value: "Failed to submit reveal ... status 400", ref: "kurtosis service logs devnet-1 buildoor -n 3000" }
      chain: ["vc-1 proposer preference", "buildoor bid-creator", "buildoor reveal-handler", "cl-1 PTC verdict slot 385"]
      edge_evidence: ["direct", "direct", "one-sided (CL logs payload id, buildoor lacks response logging)"]
      roles: { trigger: ["buildoor"], carrier: ["cl-1"], victim: ["slot 385 payload"] }
      reachability: reachable
  rejected_paths:
    - { candidate: "relay outage", reason: "no relay configured in setup", evidence: ["network_params.yaml"] }
  missing_evidence:
    - { query: "buildoor reveal response log at slot 385", would_support: "direct edge", would_reject: "different payload id" }
  confidence: medium          # scale: runbooks://evidence_discipline
```

## Trace questions

Answer explicitly: (1) Can the candidate receive the input/event that produced the
failure? (2) Does the configured topology contain every actor the path needs? (3) Is
the path gated by fork activation, client flags, builder/relay setup, validator
assignment, load, or package params? (4) Does runtime evidence show the path executed,
or only that the component was present? (5) If source was inspected, is the function
reachable from the observed RPC/log/block path at the exact runtime commit? (6) Could
another component have emitted the same symptom as downstream fallout?

## Component trace points

Use the CL/EL matrix in `runbooks://debug_ethereum_network` and the artifact model in
`runbooks://ethereum_protocol_model`. Key edges:

- **CL:** beacon API/event stream, fork-choice/head update, block validation/state
  transition, engine-API calls to the paired EL, blob/column availability, embedded VC
  duties, ePBS artifacts (bids, reveals, payload attestations, builder index).
  Confirm the active fork/slot is inside the blamed branch.
- **EL:** `engine_newPayload`/`engine_forkchoiceUpdated`, payload id
  creation/retrieval, block execution, tx/blob validation, JSON-RPC used by tools,
  peer/sync/import. If a CL log repeats an EL error, verify the EL emitted the
  corresponding evidence — the CL log may be downstream.
- **VC:** duty schedule, attestation/proposal production, builder
  registration/proposer preferences, slashing-protection/keymanager state. For
  finality stalls, VC reachability means connecting availability/duty failures to
  >1/3 effective stake (stall threshold: `runbooks://ethereum_protocol_model`) or the
  observed participation loss.
- **Builder/relay/buildoor:** registration, bid production and reveal, payload build
  errors, proposer-preference publication, relay API responses, ePBS activation and
  safe/finalized-head dependencies. Builder-path failure can be reachable WITHOUT
  stopping finality — keep builder degradation separate from chain liveness unless
  evidence links them.
- **Load generator / test runner:** configured demand and targets, submission
  success/failure, whether load makes empty blocks suspicious. An inclusion-failure
  claim requires configured demand.

## Edge evidence levels

Each chain edge is `direct` (both sides show the same request/response/duty/payload/
slot/error id), `one-sided` (one side shows it, the other lacks logging),
`topology-only` (route exists, no runtime evidence it executed), or `contradicted`
(evidence shows it could not have occurred). The level names the strength; the
citation makes it re-derivable — back every `direct`, `one-sided`, and `contradicted`
edge with an evidence item (source/ref/at/detail, `runbooks://devnet_issue_contract`)
in the issue's evidence list.

## Verdict calibration

- `reachable`: every required edge is `direct`, or `one-sided` with the missing side
  explained by normal observability limits, and the path explains the artifact.
- `partially-reachable`: topology and code path line up but a required edge is
  `topology-only`.
- `not-reachable`: a required actor, fork branch, route, assignment, or entry point is
  absent or `contradicted`.
- `insufficient-evidence`: the trace may be possible but evidence cannot distinguish it
  from alternatives.

Verdicts may be layered: when the immediate failure edge is `reachable` but the
deeper mechanism behind it is only `partially-reachable`, give each path its own
reachability in `paths` and set the top-level verdict to the weaker one — confidence
binds to the deeper claim.

Cap confidence (per `runbooks://evidence_discipline`) when the exact image/commit is
unresolved, `first_bad` is prose-only, a required actor is absent from setup, the
active fork doesn't match the blamed branch, evidence exists only on a downstream
component, or identity leans on a node name/port rather than role + client.

## Self-Check

Before returning:
- The trace starts from an existing candidate, and `first_bad` is on the chain.
- Every required actor in the chain exists in the setup.
- Source findings carry commit status from `runbooks://ethereum_spec_source_drilldown`.
- Partial or missing edges are reflected in the verdict, confidence, and
  `missing_evidence`.
