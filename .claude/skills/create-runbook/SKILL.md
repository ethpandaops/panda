---
name: create-runbook
description: Author a reusable runbook, or extract one from a completed investigation, following this repo's runbook standard. Use whenever a multi-step procedure, diagnosis, or recipe should be captured so future agents can reuse it.
disable-model-invocation: true
metadata:
  internal: true
---

# Create Runbook

Capture procedural knowledge as a runbook in `runbooks/`, following the authoring
standard in `runbooks/AGENTS.md`. Read that file first — it defines the retrieval
mechanics, frontmatter contract, split/merge rules, fact-ownership model, contract
style, and validation. This skill is the procedure for producing one runbook.

## When to use

- After a multi-step investigation whose pattern others could reuse.
- To add a new stage to the investigation pipeline.
- To turn an ad-hoc recipe (a query pattern, an optimization) into a durable how-to.

## Steps

1. **Check it should be a new file.** Search the existing registry
   (`panda search runbooks "…"` with a few phrasings). Content that would always be
   retrieved together with an existing runbook belongs IN that runbook — the split
   axis is co-retrieval, not topic count. Only mutually-exclusive-in-use content gets
   its own file.

2. **Extract the pattern, not the incident.** Generalize: what is the reusable
   diagnostic shape? Drop run-specific ids, timestamps, and host names.

3. **Engineer the retrieval surface first.** Write the `description` (what + when,
   third person, ≤1024 chars) and 3-6 `triggers` as the literal queries a caller would
   type, packed with exact identifiers (table names, error strings, API names). Body
   sections are embedded too (per-section child vectors), so mid-document facts are
   findable — but the metadata surface dominates ranking, so it decides whether the
   runbook wins the queries it should.

4. **Make it self-contained for its task.** Restate load-bearing facts briefly with a
   pointer to their canonical owner (`runbooks://<owner>`); reserve links for adjacent
   tasks. A required fact that lives only behind a link is a bug.

5. **Write to the standard's structure**: owns-line, Inputs (liberal), Output,
   Procedure; tables for decision matrices, numbered lists for procedures, positive
   directives, decisive rule up front. If the output feeds another stage: one filled
   example, ≤10 top-level fields, summary/reasoning before verdicts, confidence as
   `low|medium|high` per `runbooks://evidence_discipline`.

## Verify

- `go test ./runbooks/...` — frontmatter, refs, uniqueness.
- `panda search runbooks "<one of your triggers>"` returns the new runbook.
- `scripts/runbook-retrieval-check.sh` still passes (add a golden query for the
  new runbook while you're there).
- The server logs `Runbook registry loaded` with the new count.

$ARGUMENTS
