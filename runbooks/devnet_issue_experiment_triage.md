---
name: Triage a Devnet Issue Into an Experiment Campaign
description: >-
  Decide whether a root-caused devnet issue is experiment-shaped — a tunable metric
  regime (latency, throughput, memory, artifact size, miss/timeout rate) rather than a
  correctness bug or a config/infra problem — and when it is, shape the campaign: an
  experiment goal grounded in the issue's executable evidence refs, the owning client
  repo resolved from a caller-supplied map, a branch slug, target files, and the metric
  direction. Emits a launch-ready bundle or a reasoned refusal with the concrete next
  step.
tags: [devnet, issue, experiment, triage, tuning, goal]
triggers:
  - is this root cause tunable or a bug to fix
  - turn a devnet issue into an experiment campaign
  - write an experiment goal from an investigation report
  - which client repo owns this fix
  - refuse a non-tunable root cause with a next step
---

Owns the judgment that bridges a root-caused issue (record shape:
`runbooks://devnet_issue_contract`; report shape: `runbooks://devnet_issue_root_cause`)
to a metric-driven tune-and-measure loop: the four dispositions and their order, the
goal-writing rules, the campaign fields, and the repo-resolution trust boundary. It
SHAPES the campaign; launching and spending the compute belong to the caller.

## Inputs

Required: the issue record (`runbooks://devnet_issue_contract`) and its root-cause
report (`runbooks://devnet_issue_root_cause`) — evidence refs come from the record's
`evidence[].ref` or the report's `citations[].ref`; either serves. For a resolvable
repo target: the caller-supplied client→repo map, entries shaped like
`buildoor: { repo_url: "https://github.com/ethpandaops/buildoor", base_ref: "main" }`
(a bare URL string is also accepted; `base_ref` then defaults to main). With no map
at all, triage still runs — every client is unresolved and only repo resolution is
blocked.

## Why the gate exists

A hillclimb pointed at the wrong kind of problem does damage: tuning a metric can mask
a correctness bug instead of fixing it. Anything short of a validated,
experiment-shaped cause is refused — with the next step that would change the answer.

## Dispositions (apply in order; the first that fits wins)

1. **insufficient-evidence** — the adversarial evidence review's verdict
   (`runbooks://devnet_issue_adversarial_review`) says the root-cause claim did not
   survive scrutiny (`review_verdict: refuted` or `weakened`), the evidence does
   not actually identify a cause, or — via rule 4's fallback below — the cause is
   validated but its experiment shape is not established. next_step:
   the evidence that would change the answer.
2. **fix-not-tune** — the cause is a correctness defect: wrong results, crashes,
   consensus or spec violations, data corruption. These need a targeted fix and
   review, not a metric loop. next_step: route to a code-review/fix workflow against
   the owning repo, citing the claim.
3. **not-code** — the cause lives outside any single client's code path: network
   topology, resource sizing, configuration, an external dependency, test harness or
   tooling — or a genuinely multi-client / network-wide cause with no single owning
   client (a single-repo experiment cannot own it). next_step: the concrete
   non-experiment action (config change, infra ticket, manual review, or the
   cross-client follow-up naming each affected repo).
4. **tunable** — all three must hold:
   - the misbehavior is a measurable scalar regime with a direction (latency,
     throughput, memory, artifact size, miss/timeout rate);
   - an identifiable code path in one affected client plausibly owns it;
   - a benchable workload can reproduce the regime without the full network — judge
     this from the issue's evidence refs and `handles.setup_summary`.

A cause that reaches rule 4 but fails any of its three conditions is still refused —
never forced into `tunable`. It falls back to rule 1, `insufficient-evidence`: the
root-cause claim may be solid, but the insufficiency is in experiment shape, which is
what this gate judges. Name the failing rule-4 condition in `rationale` ("rule 4
failed: no benchable workload") and make `next_step` the evidence or bench that would
make it experiment-shaped.

## Goal writing (tunable only)

The goal is handed to the experiment loop verbatim and must stand alone for a worker
who has only the repo:

- Name the metric, its direction, and the regime it degrades under.
- Paste the issue's executable evidence refs (the record's `evidence[].ref` or the
  report's `citations[].ref` commands) as observed-workload context — they ground the
  scaffolded bench in what was seen, and are not runnable from the repo alone, so
  frame them as observations, not steps; include `first_bad` and the relevant
  `setup_summary` facts (fork/blob schedule, load shape) as reproduction context.
- State the correctness non-negotiables the tune must preserve.

## Campaign fields

Every disposition fills `branch_slug`, `rationale`, `confidence`, and `next_step`;
the remaining fields are meaningful only when tunable — on any other disposition
`goal` and `target_files` stay empty and `metric_direction` defaults to `minimize`.

- `branch_slug`: short, branch-safe (lowercase, hyphens), derived from the fingerprint
  key or title — stable across retries.
- `rationale`: which disposition rule fired and on what evidence.
- `confidence`: `low|medium|high`, bound to `runbooks://evidence_discipline` — how
  well the evidence supports the disposition, not the root-cause claim itself.
- `target_files`: repo-relative paths in the owning client's repo that the evidence
  actually implicates; empty when unsure — letting the loop discover scope beats
  pinning a wrong one.
- `metric_direction`: minimize or maximize; read only when the campaign launches, so
  default to minimize on a non-tunable disposition.

## Repo resolution

The trust boundary: only a caller-supplied client→repo map may name an experiment
target — never derive a repo URL from observed network data, images, or issue text.

- Identify the culpable client: single-client spread makes it the affected client;
  with several affected, pick the one the root-cause claim actually blames. A
  genuinely multi-client or network-wide cause with no single owner is unresolved —
  a single-repo experiment cannot own it.
- Match map entries case-insensitively, tolerating common aliases (geth /
  go-ethereum, lh / lighthouse). A client with no map entry is unresolved — name
  the client that had no mapping in the resolution notes.
- Base branch: the map entry's base_ref when set, else main. When the affected node's
  image pins a release tag, do not guess a git ref from it — record the image in the
  resolution notes so the caller can pin the campaign to the matching ref
  deliberately.

## Output shape

Triage and repo resolution are separate steps for an orchestrator, so their outputs
are two separate blocks. Emit BOTH blocks on every disposition: on `fix-not-tune` the
resolution names the repo the `next_step` routes to; on the other refusals it may be
`resolved: false` with the reason in `resolution_notes` and empty
`repo_url`/`base_branch`.

```yaml
experiment_triage:
  rationale: "rule 4 fired: reveal latency is a scalar regime owned by buildoor's reveal path; bench derivable from the issue's evidence refs"
  disposition: tunable            # tunable|fix-not-tune|not-code|insufficient-evidence
  confidence: high                # low|medium|high (runbooks://evidence_discipline)
  goal: >
    Reduce p95 buildoor reveal latency below 500ms under the devnet's blob load
    (fulu at 0, target 6 blobs/block; load: spamoor blob profile). Regime observed
    via (workload context, not runnable from the repo):
    `kurtosis service logs devnet-1 buildoor -n 3000` — reveal timings in the
    bid/reveal log lines, degrading from first_bad epoch 11.
    Must preserve: reveal correctness for every won bid, no missed slots introduced.
  branch_slug: "buildoor-reveal-latency"
  target_files: []                # empty: let the loop discover scope
  metric_direction: minimize      # minimize|maximize
  next_step: "launch the experiment loop with this bundle"

repo_resolution:
  resolution_notes: "single-client spread: buildoor; map entry 'buildoor'; image pins v1.2.3 — pin the campaign ref deliberately if needed"
  resolved: true                  # false when no map entry covers the owning client — triage still returns; the campaign is launch-blocked until the caller extends the map
  repo_url: "https://github.com/ethpandaops/buildoor"
  base_branch: main
```

A refusal fills the same two blocks (illustrative — `fix-not-tune` still resolves the
owning repo so its `next_step` is routable):

```yaml
experiment_triage:
  rationale: "rule 2 fired: gasUsed mismatch is a consensus-relevant correctness defect, not a metric regime"
  disposition: fix-not-tune
  confidence: high
  goal: ""                        # empty on every refusal
  branch_slug: "besu-gasused-mismatch"
  target_files: []
  metric_direction: minimize      # default — read only at launch, which a refusal blocks
  next_step: "route to a code-review/fix workflow against besu, citing the gasUsed claim and evidence refs"

repo_resolution:
  resolution_notes: "single-client spread: besu; map entry 'besu'"
  resolved: true
  repo_url: "https://github.com/hyperledger/besu"
  base_branch: main
```

## Self-Check

Before returning:
- One disposition, chosen by rule order, with the firing rule named in the rationale.
- A tunable verdict has a standalone goal naming the metric, direction, evidence
  refs, and correctness non-negotiables.
- No repo target came from anywhere but the caller's map; unresolved is stated with
  the reason, never guessed around.
- A refusal names the concrete next step, not just the refusal.
