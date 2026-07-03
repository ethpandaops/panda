---
name: Debug an Ethereum Network
description: Systematically debug any Ethereum network — local Kurtosis enclave, remote compute-hosted enclave, or live hosted devnet — by resolving a network_target, building the active fork model, establishing a health baseline, classifying the symptom, and localizing the fault to the CL, EL, engine API, or shared infrastructure. Use to diagnose splits, stalls, missed blocks, missing payloads, engine_newPayload failures, execution errors, or data-availability failures.
tags: [ethereum, devnet, debugging, consensus, execution, engine-api, forks]
triggers:
  - debug a devnet or ethereum network why did it break
  - network split nodes disagree on head
  - a client (prysm, lighthouse, teku, nimbus, lodestar, grandine, geth, nethermind, besu, erigon, reth, ethrex, nimbus-el) is forked off the network, can you investigate
  - the network_target contract for reaching a devnet
  - missed blocks or stalled finality
  - engine_newPayload invalid payload validation errors
  - is the fault in the consensus or execution client
  - one node stuck offline or out of sync
---

Owns the fork-aware debugging procedure for any Ethereum network, the `network_target`
handle it runs against, and CL-vs-EL fault localization. It reasons the same way
regardless of where the network runs — only the access layer changes.

## The network_target handle

A `network_target` is how you reach the network under study:

- `kind: local-enclave` — a Kurtosis devnet on this machine
  (`runbooks://kurtosis_devnet`).
- `kind: compute-enclave` — a Kurtosis devnet in a panda-compute sandbox; adds
  snapshot/restore (`runbooks://panda_compute_kurtosis_lifecycle`).
- `kind: hosted` — a live hosted devnet: resolved network id + published endpoints
  (`runbooks://hosted_devnet_context`).

Downstream steps consume a target and reason identically for every kind; providers are
swappable. Steps that want a capability one kind lacks (e.g. restore on a local enclave)
degrade gracefully — to live observation or historical evidence — instead of failing.

| Concern | local-enclave / compute-enclave | hosted |
| --- | --- | --- |
| Topology / services | `kurtosis enclave inspect`, `service ls` | network resource + node inventory |
| Endpoints | Kurtosis port names (`runbooks://kurtosis_devnet`) | published endpoints (beacon RPC, Dora, Forky, Ethnode) |
| Logs | `local-kurtosis` OTel, or `kurtosis service logs` | the hosted otel-logs datasource (commonly `external.otel_logs` on `clickhouse-raw`), filtered by `ResourceAttributes['network']` |
| Chain view | direct beacon / EL RPC | Dora / Forky / Ethnode + direct RPC |

## Inputs
Required: a `network_target`, and the reported symptom (or "is it healthy?").
Preferred: a timeframe or affected slot/epoch/block, and the setup (clients, tooling, load).

## Output
A classified diagnosis carrying, as named fields: the summary (what is happening),
the health baseline, the active protocol model used, the matched symptom branch, the
likely scope/layer/actor, affected components, any source disagreements, confidence,
and the next distinguishing query — every concrete claim cited
(`runbooks://evidence_discipline`).

## Procedure

1. **Resolve access** for the target kind (table above).
2. **Build the protocol model and baseline** with `runbooks://ethereum_protocol_model`:
   active fork, then — split or single fork? finalizing? participating? producing
   blocks/payloads? agreeing across clients? Classify a symptom only after the model
   says what the symptom means. If the baseline is healthy but a problem was reported,
   show the baseline and ask for the specific symptom before broad log scraping.
3. **Classify, then drill the matching branch** (several may apply).
4. **Localize the layer** with the CL/EL matrix below, holding to
   `runbooks://evidence_discipline` throughout (first cause, cited, judged against setup).

## Symptom → branch

| Symptom | Branch | First evidence |
| --- | --- | --- |
| Multiple heads/roots, fork disagreement | network split / fork-choice | head roots + finalized checkpoints across nodes; find the divergence slot and refocus the window there |
| Finalized checkpoint not advancing | finality / participation | run the finality-stall triage in `runbooks://ethereum_protocol_model` (service status, offline stake, completed-epoch participation) |
| `status=Missing` slots | missed beacon blocks | proposer duties → proposer node health/logs → sync/peers |
| Canonical block but no payload (ePBS) | missing payload | PTC verdict in block S+1, `builder_index`, buildoor logs (`runbooks://ethereum_protocol_model`) |
| Payload/block validation or engine errors | EL / engine API | CL engine-API logs + matching EL logs, block/payload detail (matrix below); rejected-artifact inventory: `runbooks://tracoor_invalid_artifact_forensics` |
| Builder bid/envelope gossip rejections (ePBS) | builder path | CL bid-admission logs + builder/buildoor logs for the same slot; the CL is often the rejector, not the fault (matrix below) |
| Missing blobs / data-column warnings | DA / PeerDAS | sidecars, PTC `blob_data_available`, data-column logs (`runbooks://ethereum_protocol_model`) |
| One node stuck/offline | node drilldown | direct sync/peers + node logs; distinguish down vs not-shipping-logs |
| One client type failing across nodes | client-specific bug | grouped logs by client + image/version, fork/spec context |

On a split, treat "offline-looking" nodes as possible minority-fork members — indexers
reflect the canonical fork only, so refocus on why the split happened.

## Localize: CL vs EL vs engine API

Start at the CL — most devnet issues originate there. Move to EL logs when the CL points
at execution: `engine_newPayload` failures/timeouts, payload validation failures,
execution sync issues, or "execution client unavailable".

| Evidence | Likely class |
| --- | --- |
| CL bid-admission errors + builder invalid-artifact logs, EL clean | builder tooling / builder-EL construction path — the CL is the rejector |
| CL errors only | consensus / fork-choice / validation, or CL client bug |
| CL engine-API errors + EL errors | payload / state-transition issue, or EL bug |
| CL clean, EL errors | EL-local or non-primary cascade (monitor; may not be primary) |
| Both layers erroring | shared dependency (disk/mem/net) or cascade |
| All nodes of one EL fail | EL client bug or execution-spec mismatch |
| All nodes of one CL fail | CL client bug or consensus-spec mismatch |
| Errors begin exactly at a fork epoch | fork activation / config / spec compatibility |

Engine-API failure surface: `engine_newPayload`, `engine_forkchoiceUpdated`, payload-id
creation, payload retrieval; block execution, tx/blob validation, state transition, fork
rules. If a CL log repeats an EL error, verify the EL emitted the corresponding runtime
evidence — a CL echo may be downstream, not the origin.

Client log-format fingerprints — Lighthouse: timestamped `INFO/WARN/ERRO`. Prysm:
`level=… msg=…`. Teku: Java/log4j. Geth: `LEVEL [MM-DD|HH:MM:SS.mmm] …`.
Builder/buildoor: `level=… msg="…" component=…`.

## Escalation

When evidence points at protocol semantics or exact client code, escalate to
`runbooks://ethereum_spec_source_drilldown` — but only after runtime classification.
Keep obvious config, lifecycle, tooling, and load failures out of source dives.
