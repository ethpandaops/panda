---
name: Ground Findings in Re-derivable Evidence
description: The evidence rules for any investigation — re-derivable citations, verbatim output, treating source disagreement as evidence, anchoring on the first cause, judging against the setup, and the low/medium/high confidence scale every report uses. Use for any diagnosis or report that names a concrete artifact or states a confidence.
tags: [evidence, citations, confidence, investigation, method, reporting]
triggers:
  - how should findings be cited
  - two datasources disagree which one to trust
  - how confident should the report be
  - root cause vs co-present symptom
---

Owns the evidence and reporting discipline shared by every investigation, including the
confidence scale. Reference whenever a finding names a concrete artifact.

## Rules

- **Re-derivable citations.** Every finding that names a concrete artifact (node, client,
  slot, epoch, validator, root, tx, builder, log pattern) MUST carry a citation — a
  command, query, endpoint, or file+commit that re-derives it. Claim only what you can
  re-derive. Discover the command surface with `--help` rather than hardcoding flags
  from memory.
- **Verbatim output.** Paste raw tool output in fenced blocks; keep values, names,
  counts, roots, and log lines exactly as emitted. If a response can't be pasted as-is,
  say so.
- **Disagreement is evidence.** If two sources disagree, report the disagreement and what
  each is evidence FOR (e.g. an indexer's participant list vs a log shipper's host list),
  then verify against a third source where possible — silently picking one hides the signal.
- **First cause over loudest symptom.** Anchor on the earliest slot/block/log event that
  explains later symptoms; carry chronic errors that predate or outlive the incident as
  separate co-present signals, not the root cause.
- **Separate facts / inferences / decisions.** Report raw observation before summarizing;
  label interpretation explicitly; record rejected hypotheses to prevent misattribution.
- **Judge against the setup.** Empty blocks, idle mempools, missing blob traffic, or no
  builder activity are normal when the setup lacked demand or that actor — treat them as
  issues only under configured demand. Reconstruct the setup summary before judging.
- **One active timeframe.** Use a single window across related steps; a split or fork
  boundary overrides it to the divergence point; record any change.
- **State what the evidence supports, no more.** If evidence only narrows to a class,
  say so and name the next distinguishing query. Report failed reproductions as failed,
  and queue the missing work instead of dressing weak evidence as a confident report.

## Confidence scale

Every report states confidence as one of three levels, bound to these criteria:

- **high** — reproduced, or every load-bearing claim carries a re-derivable citation,
  and component identity (role + client + image/commit) is resolved.
- **medium** — load-bearing claims are cited but one dimension is inferred: an exact
  version, a topology dependency, a boundary relation, or a missing independent
  second signal.
- **low** — plausible narrative, but identity or a key edge rests on a raw timestamp, a
  node index/port/name (rather than role + client), an unresolved image/commit, an
  unknown active fork, or a single uncited log line.

State the criterion that caps the level ("medium: exact commit unresolved"), so the
reader knows what evidence would raise it.
