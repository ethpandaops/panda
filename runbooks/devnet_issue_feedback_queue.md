---
name: Queue Devnet Investigation Follow-ups
description: Convert unresolved gaps at the end of an investigation — required next queries, partial reproductions, low-confidence source findings, missing handles — into concrete, bounded follow-up tasks an orchestrator can requeue without reading prose. Use at the end of collation, evidence review, or a root-cause report, never as the entry point for debugging a symptom.
tags: [devnet, issue, feedback, queue, follow-up, orchestration]
triggers:
  - what follow-up tasks remain after this investigation
  - turn open questions into follow-up tasks
  - investigation blocked what work remains
  - queue the unresolved gaps as bounded tasks
---

Owns the FEEDBACK-TASK shape: the handoff from "what is missing" to "the next bounded
unit of work". Run at the end of `runbooks://devnet_issue_collation`, evidence review,
or a `runbooks://devnet_issue_root_cause` report. It SHAPES tasks; execution belongs to
the target workflows.

## Inputs
Any of: `required_next_queries` from evidence review; a report's `next_action`;
low-confidence source findings; a trace verdict + `missing_evidence`; partial/failed
reproduction details; the fingerprint decision; and the available handles (devnet,
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
      kind: investigate        # watch|investigate|config|snapshot|source-trace|reachability-trace|manual-review|publish|no-op
      reason: "evidence review requires the missing direct edge before publish"
      priority: high           # high|medium|low
      inputs: { snapshot_id: "snap-9", slot: 385, service: "buildoor" }
      success_condition: "reveal response for payload id at slot 385 captured"
      stop_condition: "one restore + one bounded log/API pull"
      runbook_refs: ["runbooks://devnet_issue_root_cause"]
      blocked_by: ""
```

`kind` comes from the enum above — reproduction-oriented work maps to `investigate`
(bounded runtime verification), `config` (config-fidelity gap), or `snapshot` (missing
broken-state starting point). `inputs` carries handles and query params only. Every
task closes ONE named gap and has both a success and a stop condition.

## Mapping rules

**Required next queries** → the narrowest kind: local/restored runtime query →
`investigate`; broader window → `watch`; missing setup/config field → `config`; missing
broken state → `snapshot`; exact image/commit/code path → `source-trace`; connecting a
finding to the observed failure → `reachability-trace`; ambiguous operator policy or a
destructive action → `manual-review`. Preserve each query's support and reject conditions.

**Partial reproduction:** shifted slot/epoch → `investigate` with a narrowed window;
different network shape → `config`; timing/load dependent → `watch` with explicit load
and epoch window; remaining gap is source reachability → `reachability-trace`.
**Not reproduced:** unfaithful local args file → `config`; no broken-state snapshot →
`snapshot`; hosted devnet still observable non-destructively → `investigate`; otherwise
`terminal=true`, reason `non-reproducible-with-current-handles`.

**Low-confidence source findings:** unresolved runtime image → `source-trace`;
inspected path not tied to a runtime input → `reachability-trace`; unclear spec rule →
`source-trace` with an EIP/spec search target; contradicted by runtime evidence →
`manual-review` or `no-op`. Queue further source work only when the report would claim
a client/protocol bug.

**Report next_action:** file client bug → `publish` only when reproduction, trace, and
citations are strong (queue the missing trace/source work first otherwise); config
fix → `config`; tooling fix → `config` or `manual-review`; rerun with more evidence →
`watch` or `investigate` scoped to the missing evidence; no issue reproduced →
`no-op`, or `snapshot` if a broken-state start would change the outcome.

**Fingerprint decision:** `duplicate` → occurrence-attachment via
`publish`/`manual-review` only (plus `watch` only for a missing variant dimension);
`variant` → the smallest task that distinguishes it; `insufficient-context` → queue
the missing context first (setup, `first_bad`, components, citations).

## Priority

High: the task would confirm or refute a final-report root-cause claim, resolve a
missing runtime commit behind a claimed client bug, complete the trace needed before
publishing, or capture a missing broken-state snapshot for a reproducible serious
issue. Medium: variant analysis across versions/topology, an extra watch window for
calibration, config fidelity after partial reproduction. Low: report polish, extra
citations, observability improvements after review already survives.

## Terminal

Set `terminal=true` when: evidence review survives and no next queries remain;
reachability is demonstrated and the report is publishable; the issue is a duplicate
needing no variant info; all useful non-destructive evidence is collected and
compute/source access is unavailable; or the remaining step needs human policy approval
or a destructive action (name the blocker in `terminal_reason`). Terminal means no
useful bounded follow-up remains with the available handles — low confidence alone is a
reason to queue work, not to stop.

## Self-Check

Before returning:
- Every task kind is from the enum; reproduction work is expressed as
  `investigate`/`config`/`snapshot`.
- Every task has one named gap, a success condition, and a stop condition.
- High-priority tasks affect final confidence or publishability.
- Blockers are explicit; ordinary uncertainty is queued, not declared terminal.
