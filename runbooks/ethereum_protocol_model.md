---
name: Model the Active Fork and Judge Network Health
description: The Ethereum protocol reference — which actors, artifacts, and invariants each protocol upgrade introduces (pre-ePBS vs Gloas/ePBS vs Fulu/PeerDAS), plus the finality and participation thresholds for judging whether a network is healthy. Use when interpreting what a symptom means under the active fork's rules — missed slots, missing payloads, finality stalls, low participation, builder failures, or data-availability warnings.
tags: [ethereum, forks, epbs, gloas, peerdas, finality, participation, health]
triggers:
  - is the network healthy or finalizing
  - why is finality stalled or delayed
  - canonical block but execution payload missing on gloas epbs
  - participation below two thirds threshold
  - blob sidecar or data column availability failing on fulu peerdas
  - builder failed to reveal payload
---

Owns the protocol model and the health thresholds. Read it before interpreting any
Ethereum network symptom — a symptom only means something under the active fork's rules,
and a health verdict only means something against these thresholds.

## Build the model first

Start from the network's own configuration, not a fixed mental model ("missed slot =
proposer failed"). Write a short, explicit active protocol model — active fork, nearest
boundary, new actors, new artifacts, invariants that must hold — before classifying
symptoms. Read slot timing (seconds/slot, slots/epoch) and fork epochs from the beacon
`/config/spec`; every network differs from mainnet defaults.

## Slot model by era

- **Pre-ePBS**: binary — a slot has a block or is missed.
- **Gloas / ePBS (EIP-7732)**: three slot states — beacon block + payload (healthy);
  missed beacon block (no block; proposer node); canonical block but payload absent
  (builder/buildoor path). A canonical beacon block can exist WITHOUT a payload.
- **Fulu / PeerDAS**: adds data-column availability as a separate concern.

Gloas in the fork schedule, or `signed_execution_payload_bid` in block bodies, means
the slot model has changed.

## ePBS / Gloas glossary

- `builder_index`: an on-chain REGISTRY index, NOT a validator index — map it through
  builder sidecar logs, never via `/validators/{index}`.
- self-build: `builder_index = 18446744073709551615`. A missing payload on self-build
  points to the proposer/local-EL path, not an external builder.
- bid: `signed_execution_payload_bid` — a commitment/value, not the payload.
- reveal / envelope: builder publication of the signed execution payload envelope.
- PTC (Payload Timeliness Committee): the authoritative verdict on payload presence.
- `payload_attestations`: carried in the NEXT block (slot S's verdict is in block S+1);
  carry `payload_present` and, when available, `blob_data_available`.
- missing payload: a canonical beacon block whose committed payload was not revealed
  in time.

## Reading the builder path

- **builder-path-degraded ≠ liveness failure.** Head and finality can advance while a
  configured builder/VC repeatedly fails to produce/reveal/register. Report that as a
  separate concern, not "network healthy."
- One `builder_index` concentrating absent payloads is a builder reveal problem until
  proven otherwise; a few missing payloads do not by themselves explain a finality stall.
- Builder-side log signals: `Failed to submit reveal ... status 400` (serialization/
  version mismatch); `Giving up on reveal after max attempts` (reveal path failed);
  `Published data columns to 0 peers` (DA/peering). Builder-sidecar components:
  bid-creator, scheduler, reveal-handler, builder-service, epbs-service, bid-tracker.
- Distinguish chronic stub/not-implemented builder errors from errors that begin only
  AFTER a finality/safe-head stall — the latter may be downstream, not the cause.

## Data availability (Fulu / PeerDAS)

Inspect sidecars, data columns, custody, and blob counts separately; on ePBS check PTC
`blob_data_available`; if payload reveal fails with DA warnings, correlate builder and
DA logs before blaming payload construction.

## Health thresholds

- Finality requires **>66.7% (2/3)** of effective stake attesting correctly.
- Normal finality lag ≈ **2 epochs**. **>4** epochs is concerning; **>8** is a
  significant issue.
- If ≤2 epochs behind, the network is finalizing normally — report healthy and stop
  unless there is another symptom.

## Trust rules for health data

- Judge participation on the most recent **completed** epoch; the head epoch is in
  progress and reads artificially low (use head only to spot recent offline proposers,
  as preliminary).
- Treat finality **checkpoints** as the baseline; decide finality from checkpoints, not
  from vote/participation aggregates.
- **Source-inconsistency rule:** if an epoch marked finalized reports participation
  <66.7%, distrust that source and verify against checkpoints or another datasource.
  Copy any data-quality warning into the report.
- Verify current status first — a stall may have self-resolved.

## Finality-stall triage (cheapest first)

Test these before any client-bug or spec hypothesis:

1. Is the head still advancing while checkpoints freeze (a stall), or is it a full halt?
2. Check CL/EL/VC service status — a stopped/restarting/disconnected VC subset is the
   most common cause.
3. Estimate the offline validator/stake fraction. Offline validators above **1/3** of
   active stake stall finality even while head and execution blocks continue normally —
   classify as lifecycle/participation first, consensus as the affected layer.
4. Collect at least one independent participation signal for a completed epoch (explorer
   participation, direct validator/attestation evidence, metrics, or logs of expected vs
   produced attestations). Checkpoint stagnation alone is not proof of cause.

See `runbooks://evidence_discipline` for citation and disagreement rules, and
`runbooks://debug_ethereum_network` for the full symptom-driven procedure.
