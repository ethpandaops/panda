---
name: Root-Cause a Devnet Issue
description: Orchestrate the investigation of one Ethereum devnet issue — reproduce or restore it, run bounded hypothesis tests, escalate to spec/source drilldown when evidence demands it, trace reachability before blame, pass adversarial review, and produce a cited root-cause report. Use for single-issue investigation after collation, or on a manually reported issue.
tags: [devnet, issue, root-cause, reproduction, hypotheses, orchestration]
triggers:
  - find the root cause of a devnet issue
  - reproduce and investigate a reported bug
  - why did the devnet break investigate this issue
---

Orchestrates root-cause investigation for ONE issue in the shape of
`runbooks://devnet_issue_contract`. This runbook owns sequencing and the final report;
each stage owns its own contract — read the stage runbook when you reach that step:
fingerprint (`runbooks://devnet_issue_fingerprint_dedupe`), spec/source escalation
(`runbooks://ethereum_spec_source_drilldown`), reachability
(`runbooks://devnet_issue_reachability_trace`), critique
(`runbooks://devnet_issue_adversarial_review`), follow-ups
(`runbooks://devnet_issue_feedback_queue`).

## Inputs
Required: one issue (title, evidence, suspected category/layer). Split multi-symptom
input into separate issues unless one clear first cause links them.
Preferred: a full issue record — `first_bad`, affected components, handles
(snapshot/sandbox/enclave/network), setup summary, citations.

## Output
The final report below. State what the evidence supports and no more: a failed
reproduction or low confidence is reported as such, with the evidence that would most
improve it queued as feedback (`runbooks://evidence_discipline`).

## Core rules

1. **One issue.** Explain the issue's `first_bad` artifact; later finality loss, log
   floods, and missing blocks may be consequences.
2. **Fingerprint before expensive work.** A `duplicate` with a matched prior issue and
   no new variant dimension is a valid early exit: return the fingerprint block,
   occurrence evidence, and a `publish`/`manual-review` feedback task.
3. **Reproduce or restore before blaming.** Prefer the watch snapshot; otherwise pick
   the cheapest faithful target kind (`runbooks://debug_ethereum_network`) and label the
   mode explicitly: `reproduced | partial | not-reproduced | local-live | hosted-live |
   historical-only` (naming the historical evidence).
4. **Hypotheses are bounded.** 3–5 concrete hypotheses, each with an angle, a test, a
   supporting observable, AND a rejecting observable.
5. **Judge twice.** Adversarial plan review before compute-heavy work; adversarial
   evidence review before final claims.
6. **Escalate to specs/source only when evidence requires it** — client-specific,
   cross-client, fork-boundary, invalid-block, engine-API, DA, builder/ePBS, or
   protocol-semantics questions. Config, lifecycle, tooling, and load failures are
   closed with runtime evidence alone.
7. **Trace before blame.** Run the reachability trace before naming any component,
   topology, or protocol cause — source inspection alone shows a path exists, not that
   this network executed it.
8. **Citations are executable** (`runbooks://evidence_discipline`), and unresolved work
   becomes structured feedback tasks, never prose-only next steps.

## Procedure

1. **Canonicalize** the input into the issue record shape and fill the fingerprint
   block. Preserve upstream snapshot handles and evidence verbatim; add fields without
   rewriting facts.
2. **Choose the reproduction path** (first viable): restore the broken-state snapshot
   (`runbooks://panda_compute_kurtosis_lifecycle`); reuse an existing enclave
   (`runbooks://kurtosis_devnet`); relaunch the provided config and drive it to the
   window; synthesize a faithful config (`runbooks://hosted_devnet_context` +
   `runbooks://kurtosis_devnet_config`); or investigate the hosted network live
   (`runbooks://debug_ethereum_network`). When replaying rather than restoring, record
   the non-determinism sources (validator assignment, peer/builder timing, load, image
   drift, fork epochs).
3. **Build 3–5 hypotheses** across consensus / execution / tooling-builder /
   network-infra / config / spec angles; use the CL-vs-EL matrix in
   `runbooks://debug_ethereum_network` and start at the CL. For `finality-stall`, test
   the cheap validator-availability hypothesis FIRST (stall triage in
   `runbooks://ethereum_protocol_model`) before any client-bug or spec hypothesis.
   Then run plan review (`runbooks://devnet_issue_adversarial_review`) and revise.
4. **Reproduce against a declared success condition** (same error at the same boundary,
   same client pair disagreeing on the same block, finality stalling near the same
   epoch for the same reason). If it does not reproduce, record what was tested and why
   it is inconclusive; re-inducing a recovered transient needs caller approval.
5. **Run the hypothesis tests.** Each returns: hypothesis id, supported
   (`yes|no|partial`), evidence, rejecting evidence, remaining uncertainty, next query,
   confidence — and answers only its planned claim. For finality stalls, checkpoint
   stagnation alone is not proof; collect one independent participation signal.
6. **Spec/source drilldown** when plan review asked for it, a protocol/client-code
   question is unresolved, or the report would name a client/protocol bug.
7. **Reachability trace** whenever source/spec produced a candidate cause, a hypothesis
   claims one component triggered another's symptom, or the report names a
   component/topology/protocol cause. A partial/unknown trace caps confidence and
   queues the missing edge.
8. **Adversarial evidence review**, then incorporate its verdict.
9. **Feedback queue** over remaining gaps.

## Final report

`summary` first — the reasoning in prose — then the classified result:

```yaml
report:
  summary: >
    Finality stalled at epoch 12 because ~45% of stake (teku VCs vc-2/vc-3) entered a
    restart loop at epoch 11; reproduced on restore; participation recovered when the
    VCs were held up. Buildoor reveal errors are chronic and unrelated (co-present).
  root_cause: { statement: "teku VC restart loop removed >1/3 stake", family: "lifecycle/participation", confidence: high }
  reproduction: { status: reproduced, recipe: ["restore snap-9", "observe epochs 11-14"] }
  timeline: ["epoch 11: vc-2 first restart (log ref)", "epoch 12: checkpoints freeze"]
  hypotheses: { supported: ["h1"], rejected: ["h2 consensus bug — participation math explains loss"], partial: [] }
  trace_verdict: reachable
  review_verdict: survives
  feedback: { terminal: true, terminal_reason: "cause demonstrated; config fix filed", tasks: [] }
  citations: ["kurtosis service logs devnet-1 vc-2-teku -n 3000", "GET /api/v1/epoch/12"]
  next_action: "config fix"   # client bug | spec issue | config fix | tooling fix | rerun with more evidence | no issue reproduced
```

## Self-Check

Before returning:
- Exactly one canonical issue; fingerprint ran before reproduction or source work.
- A duplicate with no new variant dimension got the early exit, not a re-investigation.
- Source/spec findings were reachability-traced before final blame.
- Adversarial evidence review ran before the report.
- Unresolved gaps are structured feedback tasks, and confidence follows
  `runbooks://evidence_discipline` with the capping criterion named.
