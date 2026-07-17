---
name: Root-Cause a Devnet Issue
description: Orchestrate the investigation of one Ethereum devnet issue — reproduce or restore it, run bounded hypothesis tests, escalate to spec/source drilldown when evidence demands it, trace reachability before blame, pass adversarial review, and produce a cited root-cause report. Use for single-issue investigation after collation, or on a manually reported issue.
tags: [devnet, issue, root-cause, reproduction, hypotheses, orchestration]
triggers:
  - find the root cause of a devnet issue
  - reproduce and investigate a reported bug
  - investigate this collated issue record end to end
  - why did validators fail to produce blocks on my kurtosis devnet
  - investigate missed blocks on a local enclave
  - which snapshot should I restore to investigate an issue
---

Orchestrates root-cause investigation for ONE issue in the shape of
`runbooks://devnet_issue_contract`. This runbook owns sequencing and the final report;
each stage owns its own contract — read the stage runbook when you reach that step:
fingerprint (`runbooks://devnet_issue_fingerprint_dedupe`), spec/source escalation
(`runbooks://ethereum_spec_source_drilldown`), reachability
(`runbooks://devnet_issue_reachability_trace`), critique
(`runbooks://devnet_issue_adversarial_review`), follow-ups
(`runbooks://devnet_issue_feedback_queue`). Downstream of the report — reached via
`next_action`, not by the investigator — experiment triage decides whether the
demonstrated cause is a tunable metric regime
(`runbooks://devnet_issue_experiment_triage`).

## Inputs
Required: one issue (title, evidence, suspected category/layer). Split multi-symptom
input into separate issues unless one clear first cause links them.
Preferred: a full issue record — `first_bad`, affected components, handles
(snapshot/sandbox/enclave/network), setup summary, citations.

## Output
The final report below. State what the evidence supports and no more: a failed
reproduction or low confidence is reported as such, with the evidence that would most
improve it queued as feedback (`runbooks://evidence_discipline`). When the evidence
proves an immediate cause but only supports a deeper mechanism behind it, state both
in the summary and bind `confidence` to the deeper claim.

## Core rules

1. **One issue.** Explain the issue's `first_bad` artifact; later finality loss, log
   floods, and missing blocks may be consequences.
2. **Fingerprint before expensive work.** A `duplicate` with a matched prior issue and
   no new variant dimension is a valid early exit: return the fingerprint block,
   occurrence evidence, and a `publish`/`manual-review` feedback task.
3. **Reproduce or restore before blaming.** Work through these in order:
   - **Disqualifier 1 — tooling surface.** An issue classified `layer: tooling` whose
     failing component runs OUTSIDE the devnet's own enclave/network — the ClickHouse /
     CBT (ClickHouse Build Tool) datasources, the datasource side of the ingestion
     pipeline, external test-runners — is investigated on the live tooling surface
     directly (query the failing datasource or pipeline itself, e.g.
     `runbooks://clickhouse_querying`). Label the mode `tooling-live` while the
     surface still shows the failure; a recovered tooling outage investigated from its
     traces is `historical-only`. Components that run INSIDE the enclave (the
     logs-collector, spamoor/assertoor, builders) are devnet components — they take
     the normal restore/launch path below even when the issue is `layer: tooling`.
     When the failing component is not yet identified (in-enclave collector vs
     datasource-side ingestion both explain "data missing"), probe the tooling
     surface first — it is the cheap check — and commit to a path only on what the
     probe shows. When both disqualifiers apply, this one wins: the failing surface,
     not the network, is the target.
   - **Disqualifier 2 — live handle.** When a live network handle still exhibits the
     symptom AND the evidence needed to explain `first_bad` (rule 1) is still
     reachable on it, target it directly
     rather than reproducing. Label the mode by the handle's kind — `public-live` for
     a public network, `local-live` for a local or compute enclave — never
     `reproduced`, which is reserved for a symptom re-exhibited on a fresh target.
     When the needed evidence has aged out of the live target (rotated logs, pruned
     state), this disqualifier does not apply: fall through to a restore — a
     pre-`first_bad` restore is how the aged-out onset is recovered.
   - **Otherwise reproduce.** Prefer a snapshot restore
     (`runbooks://panda_compute_kurtosis_lifecycle`); otherwise pick the cheapest
     faithful target kind (`runbooks://debug_ethereum_network`). Label the mode
     explicitly: `reproduced | partial | not-reproduced | local-live | public-live |
     tooling-live | historical-only` (naming the historical evidence).
   - **Fidelity check before claiming reproduction.** Applies only when labeling
     `status` `reproduced` or `partial` on a fresh target; the live, tooling, and
     historical modes skip it. Record the target's consensus genesis time against
     the source network's — from the lifecycle output's `launch.genesis_time` for
     compute sources, or `runbooks://public_devnet_context` for public networks
     (also compare chain id when the source is public; the lifecycle output carries
     no chain id) — and put the comparison in `reproduction.recipe`, citing where
     each value came from. A MISMATCH on a supposed same-lineage restore means the
     wrong snapshot or network: stop and re-resolve the handle before any claim.
     When no source value exists, record the check as unverifiable rather than
     guessing — an unverifiable check caps `status` at `partial`.
   - **Mirrors.** Any relaunch (its genesis necessarily differs from the source's)
     and any snapshot restore are local MIRRORS of the source, not the source
     itself: record the mirror provenance in `reproduction.recipe`, keep `status`
     outcome-based (`reproduced` only when the mirror itself re-exhibited the
     symptom), and never claim reproduction on evidence the target did not produce.
   - **Restore-point choice.** Given several snapshots of the same network, choose by
     what the investigation needs: the broken-state (latest) snapshot to inspect the
     failure as it stands, or the latest snapshot strictly BEFORE the issue's
     `first_bad` to re-run the window and watch the failure develop under targeted
     observation. The epoch-0 base snapshot is not a start-of-epoch capture — check
     its recorded `captured_at` before treating it as pre-`first_bad` for an issue
     that begins inside epoch 0 (`runbooks://panda_compute_kurtosis_lifecycle`). A
     pre-`first_bad` restore re-executes rather than replays — peer, proposer, and
     builder timing re-randomize — so a failure that does not recur is a determinism
     finding to record, not a dead end.
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
2. **Choose the reproduction path** — after applying rule 3's disqualifiers in order
   (live tooling surface first, then a still-symptomatic live handle with reachable
   evidence) — first viable: restore a snapshot, picking the
   restore point per rule 3 (`runbooks://panda_compute_kurtosis_lifecycle`); reuse an existing enclave
   (`runbooks://kurtosis_devnet`); relaunch the provided config and drive it to the
   window; synthesize a faithful config (`runbooks://public_devnet_context` +
   `runbooks://kurtosis_devnet_config`); or investigate the public network live
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
  root_cause: { statement: "teku VC restart loop removed >1/3 stake", family: "lifecycle/participation", confidence: high }   # >1/3 stall threshold: runbooks://ethereum_protocol_model
  reproduction: { status: reproduced, recipe: ["restore snap-9", "observe epochs 11-14"] }   # status: reproduced|partial|not-reproduced|local-live|public-live|tooling-live|historical-only
  timeline:                      # objects, board-renderer-ready; kind: restart|block|timing|log|note
    - { ts: "2026-07-01T10:41:55Z", kind: restart, text: "vc-2 first restart", log: "<verbatim log line>" }
    - { ts: "2026-07-01T10:48:00Z", kind: timing, text: "checkpoints freeze at epoch 12" }
  hypotheses: { supported: ["h1"], rejected: ["h2 consensus bug — participation math explains loss"], partial: [] }
  trace_verdict: reachable       # reachable|partially-reachable|not-reachable|insufficient-evidence
  review_verdict: survives       # survives|weakened|refuted
  feedback:                      # shape owned by runbooks://devnet_issue_feedback_queue; always emit it embedded here — an orchestrator may ADDITIONALLY lift it out as a sibling output
    { priority_summary: "no follow-ups — cause demonstrated and reviewed",
      terminal: true, terminal_reason: "cause demonstrated; config fix filed", tasks: [] }
  citations:                     # evidence items (runbooks://devnet_issue_contract), not bare strings
    - { source: kurtosis, ref: "kurtosis service logs devnet-1 vc-2-teku -n 3000", at: "epoch 11", detail: "restart loop from 10:41:55Z" }
    - { source: dora, ref: "GET /api/v1/epoch/12", at: "epoch 12", detail: "participation 41.2% on completed epoch 12" }
  next_action: "config fix"   # file client bug | spec issue | config fix | tooling fix | experiment triage | rerun with more evidence | no issue reproduced
                              # experiment triage: the demonstrated cause looks like a tunable metric regime — the feedback queue emits an experiment-triage task handing the report + record to runbooks://devnet_issue_experiment_triage
```

## Self-Check

Before returning:
- Exactly one canonical issue; fingerprint ran before reproduction or source work.
- A duplicate with no new variant dimension got the early exit, not a re-investigation.
- Source/spec findings were reachability-traced before final blame.
- Adversarial evidence review ran before the report.
- Unresolved gaps are structured feedback tasks, and confidence follows
  `runbooks://evidence_discipline` with the capping criterion named.
