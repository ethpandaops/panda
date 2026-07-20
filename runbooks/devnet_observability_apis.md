---
name: Query Devnet Observability APIs Well
description: Query a public devnet's observability HTTP APIs — the Dora explorer API (/api/v1/epochs, /api/v1/slots), Forkmon (/data.json), Assertoor test runs — over a past window without tripping their common artifacts. list-endpoint filter params (epoch, slot, offset) silently ignored, zeroed epoch aggregates (vote_participation 0, validators 0, deposits_amount 0), boundary epochs leaking into the window, one-request-per-epoch loops that never finish. Use when scanning devnet history over HTTP.
tags: [devnet, dora, forkmon, assertoor, api, history]
triggers:
  - dora api query epochs slots over a past window
  - api filter param ignored returns the same page
  - vote_participation zero for every epoch is participation broken
  - scan devnet history without a per-epoch curl loop
  - forkmon node dark no version unhealthy
  - assertoor test runs failed during a window
---

Owns querying a public devnet's observability HTTP APIs (Dora, Forkmon, Assertoor)
over a retrospective window: filter verification, aggregate trust, enumeration
discipline, and window anchoring. Retained ClickHouse data is the richer surface when
the network has it (`runbooks://clickhouse_querying`); when two sources disagree,
reconcile with `runbooks://reconcile_chain_sources`; judging what results mean stays
with `runbooks://debug_ethereum_network`.

## Inputs
Required: the network id and a resolved window — inclusive slot/epoch range plus
wall-clock bounds.
Preferred: the deployed services and endpoints (`runbooks://public_devnet_context`).

## Surfaces

| Service | Surface | What it serves |
| --- | --- | --- |
| Dora | `/api/v1/epochs?limit=N&offset=M`, slot/epoch detail endpoints | canonical-chain history: slot status, epoch summaries, duties |
| Forkmon | `/data.json` | per-node head progress, versions, health as reported to the monitor; the HTML UI is JS-rendered — the JSON document is the data |
| Assertoor | test-runs API | test runs with per-task status and timings |

Endpoints follow the network's published service URLs; when a path is not documented,
discover it from the service root rather than guessing deeper paths.

## Rules

1. **Verify a filter is honored before iterating with it.** Issue two requests
   differing only in that param (e.g. `epoch=100` vs `epoch=200`); identical bodies
   mean the param is silently ignored — page with `limit`/`offset` and filter
   client-side instead. Dora deployments commonly ignore `epoch`, `slot`, and even
   `offset` on list endpoints, so a filtered loop can return the same latest page
   every iteration while looking like progress.
2. **Page, don't enumerate.** One request per epoch or slot across a day-scale window
   is hundreds of serial calls; fetch the largest page the endpoint serves and slice
   locally. If per-item requests are unavoidable, bound the count and sample the
   window rather than sweeping it.
3. **Never report from epoch aggregates alone.** Deployments serve zeroed or partial
   epoch-level fields (`vote_participation: 0`, `validators: 0`,
   `deposits_amount: 0`, undercounted `sync_participation`) while the chain is
   healthy. A catastrophic aggregate next to healthy finality is an indexer artifact
   until one slot-level read (slot detail, or the beacon API) agrees
   (`runbooks://reconcile_chain_sources`).
4. **Anchor to the window's inclusive edges.** List endpoints may return a boundary
   epoch or slot just outside the requested window; filter client-side by the
   window's slot range, and count an Assertoor run as in-window evidence only when
   its start time falls inside the window.
5. **A dark node in one monitor is not a down node.** A node with no version and
   "unhealthy" status in Forkmon is a node that is not reporting to Forkmon; verify
   with a second source of a different kind before claiming the node itself is down
   (`runbooks://reconcile_chain_sources`).

## Self-Check

Before returning:
- Every filter param relied on was verified honored.
- No finding rests on an epoch aggregate without a slot-level cross-check.
- Request count scales with pages, not with epochs or slots in the window.
- Every reported fact carries the exact request URL that re-derives it
  (`runbooks://evidence_discipline`).
