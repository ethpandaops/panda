---
name: Build a Devnet Consensus Bug Board
description: Scan a devnet for consensus issues (missed blocks, orphaned blocks, reorgs, participation drops, splits) over a pinned time window, cluster them into ranked bugs, run one bounded root-cause investigation per confirmed bug, and assemble the bug objects that the bug-board renderer turns into one interactive HTML leaderboard. Use for periodic devnet status reports, incident roundups, and building a CL/EL client bug knowledge base.
tags: [devnet, consensus, bug-report, scan, severity, orchestration]
triggers:
  - generate a devnet consensus bug report
  - scan the devnet for missed blocks reorgs splits and build a bug board
  - periodic devnet status report or incident roundup
  - build a CL EL client bug knowledge base
  - cluster and rank devnet issue candidates by severity
  - find issues on a hosted devnet
prerequisites: [clickhouse-raw, dora]
---

Owns the devnet consensus **bug-board pipeline**: scan a network for consensus issues
over a pinned window, cluster and rank the candidates, run one bounded root-cause
investigation per confirmed bug, and emit one bug object per bug into
`/workspace/bugs.json`. Consumes a `network_target`; rendering and publication of the
HTML board belong to `runbooks://devnet_bug_board_html`. Per-bug investigation belongs
to `runbooks://devnet_issue_root_cause`; the embedded issue record to
`runbooks://devnet_issue_contract` — this runbook owns the scan queries, the severity
rubric, the bug-object schema, and the hand-offs.

## Inputs

- **`network_target`** — required. For a hosted devnet resolve it with
  `runbooks://hosted_devnet_context` (`networks://active`, then the network resource);
  for a local enclave use `runbooks://kurtosis_devnet`; for a compute enclave use
  `runbooks://panda_compute_kurtosis_lifecycle`. Do not guess.
- **Time window** — the reporting period. Use the user's window verbatim if given;
  otherwise default to the past 4 epochs and state the default. Pin one concrete
  slot/epoch range and reuse it across every scan query; record it in the report
  header. The finality baseline is read from the chain's CURRENT checkpoints and is
  not limited by the window — a stall longer than the window still classifies at its
  full length.

## Output

`/workspace/bugs.json` — a list of bug objects in the schema below — plus the pinned
window, the health baseline, and the Phase 2 summary table. Hand the file to
`runbooks://devnet_bug_board_html` to render and publish the board. A report on a
healthy network is a valid outcome: an empty list plus the baseline.

## Procedure

1. **Scan** — collect consensus issue candidates over the window.
2. **Summarize** — cluster candidates into bugs, rank by severity, present the
   summary, and prompt the user for which to investigate.
3. **Investigate** — one root-cause investigation per confirmed bug; each returns one
   bug object.
4. **Hand off** — write all bug objects to `/workspace/bugs.json` and render with
   `runbooks://devnet_bug_board_html`.

## Phase 1: Scan For Consensus Issues

First establish a baseline (split? finalizing? participating?) by building the
protocol model with `runbooks://ethereum_protocol_model` — judge finality from
checkpoints, participation on completed epochs only. For an enclave target that was
already watched, the observation lanes from `runbooks://devnet_watch` seed the
candidate table directly.

Then collect issue candidates over the window using the examples index; do not
hardcode Dora/Forky/ClickHouse queries from memory. On raw-only devnets (no refined
database, CBT coverage 404s — `runbooks://clickhouse_querying`), treat CBT example
hits as query-shape guidance and translate them to the network's raw tables at their
actual placement.

| Candidate class | What to collect | Find the query |
| --- | --- | --- |
| Missed blocks | `status=Missing` slots, scheduled proposer, node/client | search the examples index for "missed slots over a time range" |
| Orphaned blocks | blocks produced but non-canonical (reorged out), orphan count per epoch | search the examples index for "orphaned blocks and reorgs" |
| Reorgs / splits | competing head roots, fork-choice divergence, depth | search the examples index for "network splits fork choice" |
| Participation drops | per-epoch target/head participation below threshold | search the examples index for "attestation participation by epoch" |
| Client/EL validation errors | `INVALID` payloads, gasUsed/receiptsRoot/BAL mismatches in logs | search the examples index for "recent node errors" |

For every candidate record the class, exact slot/epoch(s), scheduled proposer or
affected block root, implicated node/client, and the query that produced it. Keep the
raw rows — they become evidence items and timeline events. A split is itself top
severity; record source disagreements rather than silently picking one
(`runbooks://evidence_discipline`).

## Phase 2: Cluster And Summarize

Cluster candidates into bugs before reporting so one root cause is not counted fifty
times — missed/orphaned slots sharing a proposer node, client type, validator range,
or contiguous slot run are usually ONE bug (grouping recipe:
`runbooks://devnet_issue_fingerprint_dedupe`). Rank each with the rubric below, assign
stable ids (`MISS-01`, `ORPH-01`, `SPLIT-01`, `PART-01`, `EL-01`), and emit a summary
table: `id | class | severity | window | affected | count | one-line`.

Then **prompt the user**: present the summary and baseline, and ask which bugs to
investigate (default: everything `major` and above). Do not start investigations until
the user confirms — investigation is the expensive step.

### Severity Rubric

Severity ranks the board; it is not evidence confidence (that lives on the issue
record). The thresholds restate the finality math owned by
`runbooks://ethereum_protocol_model`.

| Severity | Signal |
| --- | --- |
| `critical` | network split, finality stalled >8 epochs, or one client fully off the canonical chain |
| `major` | participation below the 66.7% finality threshold, finality lag 4–8 epochs, a miss/orphan streak concentrated on one client type |
| `minor` | isolated single-node misses, orphan count in normal churn range, errors with no chain symptom |

## Phase 3: Per-Bug Investigation

For each confirmed bug, run ONE bounded investigation — concurrently where the surface
supports parallel work, otherwise sequentially. Each investigation:

1. Canonicalizes its candidate rows into an issue record
   (`runbooks://devnet_issue_contract`) and runs
   `runbooks://devnet_issue_root_cause` **scoped to this one bug's window and symptom
   class** — fingerprint early-exit, reproduction, hypothesis tests, trace, and
   adversarial review all apply. Do not substitute a raw diagnosis for the root-cause
   report: board rows that skipped root-cause must carry `status_badges` of kind
   `draft`, never `confirmed`.
2. Obeys every `runbooks://evidence_discipline` rule — re-derivable citations,
   verbatim output, first cause over loudest symptom, data-quality caveats.
3. Returns exactly one bug object per the schema below. Map the root-cause report
   into it: `report.summary` → `board.overview_text`, `report.root_cause.statement`
   (plus supporting reasoning) → `board.root_cause_text`, `report.timeline` objects →
   `timeline` (already renderer-shaped: ts/kind/text/log), `report.citations` →
   `issue.evidence`, and report verdicts (reproduced / reachable / review survives) →
   `board.status_badges`. TRANSLATE `report.reproduction.recipe` into `repro` blocks —
   each `{ title, code }` with runnable sandbox Python, not prose steps. **Omit any
   field the evidence does not support — never fabricate a PR, image, flag, source
   line, or EIP commit.**

Hand-off contract — give each investigation the `network_target`, the frozen window,
the bug id/class, the candidate rows already collected, and this schema. It is a shape
to copy — values are illustrative; drop any field you cannot support. All `*_text`
fields are PLAIN TEXT (blank line = paragraph break); the renderer HTML-escapes
everything.

```yaml
bug:
  id: MISS-01                        # stable id from the Phase 2 summary table
  issue:                             # one issue record — full shape owned by runbooks://devnet_issue_contract
    # TRUNCATED example: emit the FULL record — also first_bad, affected,
    # co_present, fingerprint, and handles, copied from the contract's example.
    summary: >
      Slots 4711-4740 scheduled on geth-paired proposers are all Missing; the first
      bad artifact is slot 4711, the Gloas activation epoch. engine_getPayload times
      out on all four geth ELs while both lighthouse-geth and teku-geth pairs are
      affected, implicating the EL side. Reproduced against the frozen window.
    title: "geth proposers miss every slot after the Gloas boundary"
    classification: { category: missed-proposals, layer: execution, spread: single-client }
    evidence:
      - { source: clickhouse-raw, ref: "SELECT slot, status, proposer FROM ... WHERE slot BETWEEN 4711 AND 4740", at: "slot 4711", detail: "status=Missing on all 30 geth-proposed slots" }
    confidence: high                 # scale: runbooks://evidence_discipline
  severity: major                    # critical|major|minor — rubric above
  board:                             # presentation fields this runbook owns — *_text is plain text, escaped by the renderer
    subtitle: "engine_getPayload timeouts on geth after Gloas activation"
    upvotes: 0                       # baked-in snapshot count; humans/agents seed it
    status_badges:                   # pills; kind ∈ open|confirmed|investigating|draft|fixed — carry the report verdicts here
      - { text: "confirmed", kind: confirmed }
      - { text: "reproduced", kind: fixed }
    labels: ["missing from EELS testing"]            # free-form chips, matched by text search
    connections:                     # all optional — include only what the evidence supports
      clients: ["geth EL", "lighthouse CL"]          # drives the leaderboard client filter
      instances: ["el-3-geth"]                       # → ethpandaops.io instance page + ssh line
      images:  [{ role: "EL", image: "ethereum/client-go:v1.16.1@sha256:0d2e…" }]
      flags:   [{ client: "geth", flags: "--gcmode archive" }]
      prs:     [{ repo: "ethereum/go-ethereum", number: 31021, state: open }]
      eip:     { number: 7732, commit: "a3b1c9d" }   # EIP pinned to its commit version
      source_refs: [{ repo: "ethereum/go-ethereum", ref: "v1.16.1", path: "miner/payload_building.go", line: 118, line_end: 131 }]
      kurtosis_config_url: "https://github.com/ethpandaops/<network>/blob/<ref>/network-params.yaml"
    overview_text: ""                # plain text — the renderer falls back to issue.summary
    root_cause_text: "The payload builder blocks on the archive-mode state lookup introduced at the Gloas boundary; all four geth ELs time out identically."
    fix_text: "Fix PR applies the change in the missing path — open."
  timeline:                          # kind ∈ restart|block|timing|log|note → coloured markers
    - { ts: "2026-07-01 10:42Z", kind: block, slot: 4711, text: "first missed geth slot", log: "<verbatim log line — escaped by the renderer>" }
  series:                            # rendered as inline SVG; events draw vertical markers
    - { title: "missed slots per epoch", unit: "slots", points: [[147, 0], [148, 7]], events: [{ x: 148, label: "Gloas" }] }
  repro:                             # embedded panda python the reader runs to reproduce
    - { title: "show the miss streak", code: "from ethpandaops import clickhouse\n..." }
```

If the investigation only narrows the bug to a class, say so in `issue.summary` and
cap `issue.confidence` accordingly — do not overstate certainty.

## Phase 4: Hand Off To The Renderer

Collect every bug object into `/workspace/bugs.json`, then render and publish the
board with `runbooks://devnet_bug_board_html`, passing the network id, the pinned
window, and the Phase 1 baseline. With zero bugs the board is baseline + empty list.

## Self-Check

Before delivering:

- The window was pinned once and is identical across every scan query, bug, and the
  report header; the finality baseline was read from current checkpoints.
- Every confirmed bug ran `runbooks://devnet_issue_root_cause`; anything that skipped
  it is badged `draft`.
- Every bug embeds one issue record per `runbooks://devnet_issue_contract`, with
  summary-first reasoning and a stated confidence.
- Every board field is evidence-backed — no fabricated PR, image, flag, source line,
  or EIP commit.
- `/workspace/bugs.json` was written and handed to `runbooks://devnet_bug_board_html`.
