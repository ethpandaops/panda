---
name: Adversarially Review a Devnet Investigation
description: Critique a single-issue investigation plan before compute is spent, or its evidence bundle before final claims — catching downstream-symptom testing, unfalsifiable hypotheses, unsupported blame, missing protocol context, and duplicate re-investigation. A bounded critic inside one investigation, not a parallel investigation.
tags: [devnet, issue, review, critic, judge, evidence]
triggers:
  - review this investigation plan before running it
  - does this root cause conclusion hold up
  - critique the evidence for this devnet finding
---

Owns the two REVIEW modes for a run of `runbooks://devnet_issue_root_cause`. It attacks
the plan or conclusion using evidence already collected, names missing evidence, and
forces a sharper next step — judged against `runbooks://evidence_discipline`. It works
from the inputs it is given; discovery and fact-finding stay with the investigation.

## Plan review

Run before spending compute or fanning out hypothesis tests.
Inputs: the issue record, fingerprint decision, proposed reproduction strategy,
proposed hypotheses/tests, setup summary, and available handles.

Questions: (1) Does the plan test the issue itself or a downstream symptom? (2) Would
the fingerprint's duplicate/variant decision change how much work is warranted?
(3) Is `first_bad` bounded well enough? (4) Is the cheapest faithful reproduction path
selected? (5) Are CL, EL, tooling, network, config, and fork/spec angles represented
where the category needs them? (6) Is each hypothesis falsifiable — a supporting AND a
rejecting observable? (7) Is spec/source drilldown justified by evidence, or expensive
archaeology for a config/lifecycle/load issue? (8) Will named source/runtime blame get
a reachability trace before final reporting? (9) Are citations planned for every
concrete artifact?

Output — reasoning first, same verdict scale as the evidence review:

```yaml
plan_review:
  summary: >
    The plan tests the finality stall via client-bug hypotheses before checking VC
    availability; h3 has no rejecting observable. Reproduction via full relaunch
    when snap-9 exists is wasteful.
  verdict: weakened              # survives|weakened|refuted — refuted needs a decisive, checkable break
  strongest_counterargument: "h1-h5 all test why the block was rejected; none tests why the node failed to recover for 1200+ slots"
  missing_evidence:              # same shape as runbooks://devnet_issue_reachability_trace
    - { query: "VC/service availability timeline across the stall window", would_support: "availability gap explains the stall", would_reject: "all VCs up throughout" }
```

A weakened or refuted verdict must make `strongest_counterargument` concrete enough
that a reviser can act on it — the investigation revises the plan against the review
before executing it; the review itself criticizes, it does not rewrite. Judge the
plan's substance, not its process framing: a plan noting it awaits review has not
refuted itself. Stop at criticism only when the inputs lack the minimum issue,
evidence, and reproduction target to proceed.

## Evidence review

Run after reproduction, hypothesis tests, and any spec/source drilldown.
Inputs: the issue record, reproduction result + recipe, hypothesis results, source
findings and trace result when present, citations, rejected hypotheses, feedback tasks.

Questions: (1) Reproduced, partial, or not — honestly labeled? (2) Does the root cause
explain `first_bad`, not just later fallout? (3) Cause or only correlation? (4) Were
minority clients treated neutrally where implementations disagreed? (5) Were
direct-node, indexed, log, and tooling views reconciled? (6) Do rejected hypotheses
have real rejecting evidence? (7) If code/specs were inspected, was the exact runtime
commit resolved? (8) Did a reachability trace connect the inspected path to the
failure? (9) Does every concrete claim carry an executable citation? (10) Are gaps
structured feedback tasks or explicitly terminal? (11) Is confidence on the
`runbooks://evidence_discipline` scale with the capping criterion named?

Output — reasoning first:

```yaml
evidence_review:
  summary: >
    Cause explains the first artifact and reproduced cleanly; the teku-version claim
    is uncited — downgrade or cite. Otherwise survives.
  verdict: survives            # survives|weakened|refuted
  unsupported_claims:
    - { claim: "regression introduced in teku 26.1.0", action: downgrade }   # remove|downgrade|trace|source-trace|investigate|config|watch
  required_next_queries:
    - { query: "diff teku 26.0/26.1 VC restart handling", would_support: "version regression", would_reject: "present in both" }
  confidence_adjustment: "high -> medium (uncited version claim)"
  feedback_required: true
```

## Rules

- Ground every criticism in evidence, missing evidence, or a concrete claim in the
  input; treat absence of evidence as a confidence limit, not proof of the opposite.
- Require spec/source drilldown (`runbooks://ethereum_spec_source_drilldown`) when the
  report names a client bug, protocol bug, fork-rule mismatch, invalid block, execution
  mismatch, DA issue, or cross-client disagreement; runtime evidence alone closes
  config/load/lifecycle/tooling issues.
- Require a reachability trace (`runbooks://devnet_issue_reachability_trace`) when a
  source path, component, builder/relay path, load generator, or topology condition is
  named causal; flag untraced blame for downgrade.
- A duplicate already covered by dedupe context gets occurrence-attachment, not
  re-publication as a new issue.
- Convert actionable gaps into `required_next_queries` for the feedback queue; keep
  feedback kinds within the enum owned by `runbooks://devnet_issue_feedback_queue`.
- A useful review often says the conclusion survives and explains why — over-rejection
  is as much a failure as rubber-stamping.

## Self-Check

Before returning:
- Every criticism points at evidence, missing evidence, or a concrete claim.
- Duplicate handling was checked when fingerprint context exists.
- Untraced source/runtime blame is downgraded or sent to trace.
- Actionable gaps are `required_next_queries`, ready for feedback-queue conversion.
