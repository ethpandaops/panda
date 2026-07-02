# Runbooks

Embedded markdown runbooks, served to agents through semantic search and direct read
(`runbooks://<name>`). A runbook is **retrieved context**: it lands in a working
agent's window only when matched, competes with the task for attention, and is read
without its neighbors present. Everything in this standard follows from that.

## How retrieval actually works (write for this)

- **What gets embedded:** a metadata vector — `name` + `description` + `tags` +
  `triggers` + the first paragraph (before the first `##`) + the section headers —
  plus one child vector per `##` section (its header + the start of its prose, code
  fences skipped), all resolving to the whole runbook. Lexical matching additionally
  runs over the stem, name, description, tags, and triggers. Mid-document prose IS
  findable via the section vectors, but the metadata surface dominates ranking —
  put the load-bearing phrasing in `description`/`triggers`, not deep in a body.
- **What search returns:** metadata + the `runbooks://` ref only. The agent then reads
  the whole file.
- **What a cross-reference is:** a retrieval hint, not a hyperlink. Following one costs
  the agent another read; agents routinely don't. A runbook must therefore be
  **self-contained for its own task** — links are for adjacent tasks (escalation,
  deeper dives), never for facts required to complete the primary task.

## Frontmatter contract

```yaml
---
name: Imperative Title             # required — "Debug X", "Query Y Well"
description: What + when           # required — ≤1024 chars, third person, packed with
                                   # the exact identifiers and symptoms callers search
tags: [keyword, keyword]           # 3-6 lowercase keywords
triggers:                          # required — 3-6 example queries a caller would
  - why is finality stalled        # actually type; embedded verbatim for search
prerequisites: [clickhouse-raw]    # optional — datasources or state needed
---
```

The `description` and `triggers` are the retrieval surface — phrase them as the
questions and symptoms an agent would type ("engine_newPayload invalid", "is the
network healthy"), not as an abstract of the document. Include exact table names,
error strings, and API names verbatim: lexical matching rewards them.

## One file per task, split by co-retrieval

Split content only when the pieces are **mutually exclusive in use**; merge content
that is **always read together**. Line count is not the axis — a 30-line recipe and a
140-line pipeline stage are both fine (soft cap: 500 lines). Signs you should merge:
two runbooks that always co-fire in search, or a runbook nothing triggers on its own.
Signs you should split: a file serving two audiences that never overlap.

Each file declares one job in its owns-line (first paragraph): what it does, when to
use it, and — when it participates in a pipeline — what it consumes and emits.

## Facts: one canonical owner, restated where load-bearing

Each shared fact (a threshold, a glossary, a procedure) has exactly **one canonical
owner** where it is defined with its rationale. Other runbooks that need the fact to
complete their task **restate it briefly with a pointer to the owner** on the same
line or paragraph — e.g. ">1/3 offline stake stalls finality
(`runbooks://ethereum_protocol_model`)". The pointer is the drift backstop: when the
owner changes, grep the ref to find every restatement. A restatement without a pointer
is a bug; so is a required fact that exists only behind a link.

Keep reference chains **one level deep**: a runbook may point at an owner, but not at
a chain of owners.

## Structure

Required: frontmatter, owns-line. Then whatever the job needs, typically:
`## Inputs` (liberal — accept thin inputs and degrade gracefully; be strict only where
guessing poisons the result), `## Output`, `## Procedure`, and optionally Rules,
Non-Goals, Examples, Self-Check. Self-Check stays cheap and high-value: invariants to
confirm before returning.

Formatting that models follow best:

- **Tables for decision matrices** (symptom → branch, evidence → class).
- **Numbered lists for sequential procedures.**
- **Front-load the decisive rule** — owns-line or first section, never mid-body.
- **Positive directives.** Say what to do; when a prohibition is load-bearing, pair it
  with the alternative ("resolve the range from `fct_block_head` — a subquery on the
  partitioned table is the full scan you are avoiding").
- **Mark code blocks by intent:** a command to execute verbatim, or a shape to read as
  context. Ambiguity causes both over-reliance and skipped steps.
- Reserve MUST/MUST NOT for real constraints — safety, correctness, output invariants.

## Output contracts (pipeline stages)

When a runbook's output feeds another stage, specify it as **one filled example with
realistic values**, not an abstract schema — models copy examples more reliably than
they satisfy schemas, and there is no runtime validator here.

- **≤10 top-level fields.** Forward only what the next stage reads; stage-local
  working data stays in prose or evidence. Group related fields into small nested
  objects rather than adding top-level ones.
- **Reasoning first.** The free-text `summary` field comes before enums, verdicts, and
  scores — serializing a verdict before the reasoning degrades both.
- **Confidence is `low|medium|high`**, bound to the criteria owned by
  `runbooks://evidence_discipline` — never a 0-100 integer (uncalibrated precision).
- Shared shapes (the issue record, evidence items) live once, in
  `runbooks://devnet_issue_contract`; stages reference them instead of re-declaring.
- When an example shows a shared shape **truncated**, mark the elision inside the
  YAML itself with a comment naming the omitted required fields and the owner ref.
  Agents copy the block they see, not the prose around it — an elision visible only
  outside the example produces silently partial records.
- Note allowed enum values in a trailing comment on the field, only where the enum is
  load-bearing.

## Examples over exhaustion

A few concrete, canonical examples teach intent better than exhaustive rule lists.
Mark them illustrative. Point at an examples-index search for query patterns
instead of pasting long queries.

## Surface-neutral addressing

Runbook bodies reach every surface verbatim — the MCP tools, the CLI, and sandbox
code all read the same markdown, and no dialect rewriting happens on the way out.
Spell discovery so any reader can follow it:

- Cross-runbook reads: the bare `` `runbooks://<stem>` `` ref. Every surface can
  resolve a ref; only the CLI spells it `panda read`.
- Searches: prose — "search the examples index for 'chartkit bar chart'" — not one
  surface's invocation (`panda search …` and `search(type=…)` are both dialects).
- Literal `panda …` commands belong only where the CLI *is* the procedure
  (`panda compute`, `panda devnets`): those are operations, not addressing.

## Naming

- Filename `snake_case.md`; the stem is the stable `runbooks://` ref.
- `name`: imperative Title Case.
- Reference other runbooks as `` `runbooks://<stem>` `` (backticked) — every ref must
  resolve; tests enforce it.

## Validation

After adding or editing a runbook:

- `go test ./runbooks/...` — frontmatter loads (name, description, triggers), refs
  resolve and are unique, descriptions fit the length limit.
- The server logs `Runbook registry loaded` with the updated count; the runbook is
  then reachable via search.
- `scripts/runbook-retrieval-check.sh` (needs a running server) — the golden-query
  matrix: symptom-phrased queries must return the intended runbook top-1, including
  confusable near-miss pairs. Run it after editing descriptions or triggers.

## Maintenance

Runbooks are eval-maintained, not write-once: when an agent misfires despite having
the right runbook, codify the fix back into the runbook; when search returns the wrong
runbook, fix the description/triggers of both the false positive and the miss; a
runbook that is never retrieved gets its retrieval surface fixed or its content merged
into where agents actually look.
