---
name: Queue Devnet Investigation Follow-ups
description: Convert unresolved gaps at the end of an investigation — required next queries, partial reproductions, low-confidence source findings, missing handles — into concrete, bounded follow-up tasks an orchestrator can requeue without reading prose. Use at the end of collation, evidence review, or a root-cause report, never as the entry point for debugging a symptom.
tags: [devnet, issue, feedback, queue, follow-up, orchestration]
triggers:
  - what follow-up tasks remain after this investigation
  - turn open questions into follow-up tasks
  - investigation blocked what work remains
  - queue the unresolved gaps as bounded tasks
  - merge follow-up queues between drain rounds
---

Owns the FEEDBACK-TASK shape: the handoff from "what is missing" to "the next bounded
unit of work". Run at the end of `runbooks://devnet_issue_collation`, evidence review,
or a `runbooks://devnet_issue_root_cause` report. It SHAPES tasks; execution belongs to
the target workflows.

## Inputs
Any of: `required_next_queries` from evidence review; a report's `next_action`;
low-confidence source findings; a trace verdict + `missing_evidence`; partial/failed
reproduction details; the fingerprint decision; and the available handles (network,
config artifact, snapshot id, sandbox id, enclave).

## Output

```yaml
feedback:
  priority_summary: >
    One high task: restore snap-9 and capture the buildoor reveal response at slot 385 —
    it decides the root-cause claim. Everything else is calibration.
  terminal: false
  terminal_reason: ""
  tasks:
    - id: "fq-1"
      kind: investigate        # watch|investigate|config|snapshot|source-trace|reachability-trace|experiment-triage|manual-review|publish|no-op
      reason: "evidence review requires the missing direct edge before publish"
      priority: high           # high|medium|low
      inputs: { snapshot_id: "snap-9", slot: "385", service: "buildoor" }
      success_condition: "reveal response for payload id at slot 385 captured"
      stop_condition: "one restore + one bounded log/API pull"
      runbook_refs: ["runbooks://devnet_issue_root_cause"]
      blocked_by: ""
```

`kind` comes from the enum above — reproduction-oriented work maps to `investigate`
(bounded runtime verification), `config` (config-fidelity gap), or `snapshot` (missing
broken-state starting point). `inputs` carries handles and query params only, every value stringified. Every
task closes ONE named gap and has both a success and a stop condition.
`runbook_refs` names the workflow the task hands to, by kind: `investigate`/`snapshot`
→ `runbooks://devnet_issue_root_cause`, `watch` → `runbooks://devnet_watch`, `config`
→ `runbooks://kurtosis_devnet_config`, `source-trace` →
`runbooks://ethereum_spec_source_drilldown`, `reachability-trace` →
`runbooks://devnet_issue_reachability_trace`, `experiment-triage` →
`runbooks://devnet_issue_experiment_triage`.

## Mapping rules

**Required next queries** → the narrowest kind: local/restored runtime query →
`investigate`; broader window → `watch`; missing setup/config field → `config`; missing
broken state → `snapshot`; exact image/commit/code path → `source-trace`; connecting a
finding to the observed failure → `reachability-trace`; ambiguous operator policy or a
destructive action → `manual-review`. Preserve each query's support and reject
conditions: the support condition becomes the task's `success_condition`; carry the
reject condition in `inputs` (e.g. `inputs.reject_condition`) so a requeue gets both
without reading prose.

**Partial reproduction:** shifted slot/epoch → `investigate` with a narrowed window;
different network shape → `config`; timing/load dependent → `watch` with explicit load
and epoch window; remaining gap is source reachability → `reachability-trace`.
**Not reproduced:** unfaithful local args file → `config`; no broken-state snapshot →
`snapshot`; public devnet still observable non-destructively → `investigate`; otherwise
`terminal=true`, reason `non-reproducible-with-current-handles`.

**Low-confidence source findings:** unresolved runtime image → `source-trace`;
inspected path not tied to a runtime input → `reachability-trace`; unclear spec rule →
`source-trace` with an EIP/spec search target; contradicted by runtime evidence →
`manual-review` or `no-op`. Queue further source work only when the report would claim
a client/protocol bug.

**Report next_action:** file client bug → `publish` only when reproduction, trace, and
citations are strong (queue the missing trace/source work first otherwise); spec
issue → `source-trace` with the EIP/spec target, then `publish` once the spec
comparison is resolved; config fix → `config`; tooling fix → `config` or
`manual-review`; experiment triage → one `experiment-triage` task handing the report
and issue record to `runbooks://devnet_issue_experiment_triage` — `inputs` carries
the issue's handles and fingerprint key; the report and record themselves travel
BESIDE the queue as the investigation's sibling outputs, and the orchestrator pairs
them to the task by that fingerprint key (success condition: a disposition
exists) — on its return, a
launch-ready bundle goes to the caller (launching is not a queue task) and a
refusal's `next_step` maps back through these rules like any required next
query; rerun with more evidence → `watch` or
`investigate` scoped to the missing evidence; no issue reproduced → `no-op`, or
`snapshot` if a broken-state start would change the outcome.

**Fingerprint decision:**

- **`new`, emitted by this collation round** → NO task: the emitted issues are
  themselves the handoff — the orchestrator feeds each one to the investigator
  directly (`runbooks://devnet_issue_collation`), and a queue task here would
  dispatch the same issue twice.
- **`new`, with no other route to an investigator** (a manually reported issue
  fingerprinted outside a collation round, or a SECOND issue surfaced during an
  investigation of a different issue) → one `investigate` task; the issue record
  (`runbooks://devnet_issue_contract`) is the handoff and `inputs` still carries only
  its handles, so name the issue in `reason`.
- **`new`, for the issue a finishing root-cause report investigated** → NO task: that
  report IS the completed investigation of the new issue — never queue a fresh
  `investigate` for it (see Merging: the drain never converges otherwise). Key this
  case by issue identity, not by where the fingerprint ran: a different new issue
  surfaced mid-investigation is the case above and gets its one `investigate` task.
- **`duplicate`** → occurrence-attachment via `publish`/`manual-review` only (plus
  `watch` only for a missing variant dimension).
- **`variant`** → the smallest task that distinguishes it — with the same two
  carve-outs as `new`: a variant issue emitted by this collation round is itself the
  handoff (NO task — the orchestrator dispatches it directly), and when the finishing
  report itself already distinguished the variant dimension, that work is done —
  record the variant fingerprint with no fresh task.
- **`insufficient-context`** → queue the missing context first (setup, `first_bad`,
  components, citations).

## Priority

High: the task would confirm or refute a final-report root-cause claim, resolve a
missing runtime commit behind a claimed client bug, complete the trace needed before
publishing, or capture a missing broken-state snapshot for a reproducible serious
issue. Medium: variant analysis across versions/topology, an extra watch window for
calibration, config fidelity after partial reproduction. Low: report polish, extra
citations, observability improvements after review already survives.

## Merging queues across rounds

An orchestrator draining follow-ups round by round hands back several queues at once —
the previous round's queue, one queue per executed task, AND the embedded queue from
each directly dispatched investigation (collation-emitted issues travel outside the
task list, but their reports' feedback still merges here). Merge them into ONE
next-round queue:

- Drop each executed task; its replacement is whatever its own follow-up queue says,
  not a copy of the original.
- Two tasks that close the same named gap are one task: keep the higher-priority task
  whole (tiebreak: the tighter stop condition), then fold in only the other task's
  non-conflicting `inputs` keys — on a key conflict the surviving task's value wins,
  so its success/stop conditions always match its handles.
- An issue investigated this round re-enters only through the fingerprinting runbook
  (`runbooks://devnet_issue_fingerprint_dedupe`): a duplicate gets
  occurrence-attachment — plus a `watch` only for a missing variant dimension (see
  Fingerprint decision above) — and a variant gets at most ONE bounded distinguishing
  task per named dimension (re-randomized peer/proposer/builder timing from a
  re-executed restore is noise, not a new dimension). Neither ever gets a fresh
  investigate task, or the drain never converges.
- Re-check `blocked_by` against this round's results and clear blockers that resolved.
- Re-judge `terminal` on the MERGED queue by the rules below; per-queue terminal flags
  from individual investigations do not carry over.

## Terminal

Set `terminal=true` when: evidence review survives and no next queries remain;
reachability is demonstrated and the report is publishable; the issue is a duplicate
needing no variant info; all useful non-destructive evidence is collected and
compute/source access is unavailable; or the remaining step needs human policy approval
or a destructive action (name the blocker in `terminal_reason`). Terminal means no
useful bounded follow-up remains with the available handles — low confidence alone is a
reason to queue work, not to stop. The `manual-review` boundary: when a human decision
would UNBLOCK further bounded automated work, queue a `manual-review` task; when the
human-gated step is itself the only remaining step, set `terminal=true` instead.

## Self-Check

Before returning:
- Every task kind is from the enum; reproduction work is expressed as
  `investigate`/`config`/`snapshot`.
- Every task has one named gap, a success condition, and a stop condition.
- High-priority tasks affect final confidence or publishability.
- Blockers are explicit; ordinary uncertainty is queued, not declared terminal.
