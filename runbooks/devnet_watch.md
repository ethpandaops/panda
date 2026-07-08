---
name: Watch a Devnet and Record Facts
description: Observe a running Ethereum devnet — a local or restored Kurtosis enclave, or a live public devnet — over a fixed epoch window and collect neutral per-node and network-wide observations — consensus, execution, validator/builder, and tooling lanes — as facts for a later collation step to judge. Use to watch a devnet without deciding what is or isn't a problem.
tags: [devnet, watch, kurtosis, public, consensus, execution]
triggers:
  - watch a devnet for a few epochs
  - collect observations from a running enclave
  - watch a public devnet for a few epochs
  - monitor devnet nodes without judging issues
---

Owns neutral observation of a running devnet over a watch window. Watchers report
FACTS; judging belongs to `runbooks://devnet_issue_collation`. For interactive
debugging use `runbooks://debug_ethereum_network` instead.

## Inputs
Required: a `network_target` of any kind — `local`, `compute`, or
`public` — and a watch length in epochs. The observation lanes are the same across
kinds; what differs is the access layer and, on public targets, per-node reach (see
Setup). Resolve how to reach the target's nodes, logs, and chain view for its kind
via `runbooks://debug_ethereum_network` (the per-kind access table) and, for public,
`runbooks://public_devnet_context` (network id, endpoints, node inventory).
Preferred: the setup (clients, tooling, load) for context.

## Output
Neutral observations per lane plus a service map and setup summary (shape below).
Verdicts are out of scope — record what happened, not whether it is a problem, and
leave root-causing to downstream stages.

## Setup

- Reach and map the target for its kind — resolve topology, ports/endpoints, and logs
  via the access table in `runbooks://debug_ethereum_network`: for enclave kinds map the
  enclave and verify the rendered config with `runbooks://kurtosis_devnet`; for public
  resolve the network id, published endpoints, and node inventory with
  `runbooks://public_devnet_context` and read logs from the public-devnet otel-logs
  datasource.
  On public targets expect reduced per-node reach: per-node beacon endpoints usually
  require auth (401) and a validator→node mapping is often absent
  (`runbooks://public_devnet_context`) — record per-node facts from the otel-logs
  datasource and the node inventory, sample chain state through the published
  endpoints (Dora, Forky, checkpoint sync), and note the reduced reach in
  `watch.notes` rather than skipping the lane.
  Keep validator/builder/tooling services as their own observation lane — a devnet can
  finalize while the builder path is degraded (`runbooks://ethereum_protocol_model`).
- **Shared watch window:** resolve a beacon endpoint, read current slot/epoch and slot
  timing from `/config/spec`, set `end_epoch = start_epoch + watch_epochs`; every
  watcher stops at `end_epoch`. If a fork or BPO boundary falls inside the window,
  compute the exact boundary slot, sample before/after, and record the block `version`.

## What each lane records (facts only)

- **Consensus (per CL):** head slot/root, finalized/justified checkpoints, sync +
  peers, proposal/attestation behavior, fork-specific artifacts (bids, payload
  attestations).
- **Execution (per EL):** latest block + import, `eth_syncing`, peers, tx-pool under
  load, engine-API/payload errors. Tag by block number + timestamp (EL has blocks,
  not epochs).
- **Validators/builders (per service):** block publication, payload attestation,
  proposer preference, builder build/reveal/register/bid; whether errors start at a
  fork boundary or only after a finality/safe-head change.
- **Network-wide:** head + finality progression, cross-node agreement, service
  stops/restarts, missed slots/reorgs/splits, participation on completed epochs
  (`runbooks://ethereum_protocol_model`), block fullness relative to configured load
  (`runbooks://evidence_discipline` — judge against setup); on ePBS, payload presence;
  on PeerDAS, sidecar/column availability.

Use bounded log pulls. On enclave kinds, Kurtosis applies `-n` (tail) BEFORE `--match`,
so pull `-n 2000`+ and treat an empty filtered result as absent from the fetched tail,
not globally absent (owner: `runbooks://kurtosis_devnet`). On public targets, pull from the
otel-logs datasource filtered by `ResourceAttributes['network']` rather than
`kurtosis service logs` (owner: `runbooks://debug_ethereum_network`).

## Output shape

Free-text `notes` first, then the structured lanes. Each observation is an evidence
item (`runbooks://devnet_issue_contract`) plus the lane's role/service labels:

```yaml
watch:
  notes: >
    Window epochs 10-14 on devnet-1. Heads advanced on all 4 CLs; checkpoints froze
    at epoch 12. vc-2/vc-3 restart-looping from epoch 11. Load generator idle by
    config — empty blocks expected.
  window: { start_epoch: 10, end_epoch: 14, seconds_per_slot: 6, slots_per_epoch: 32 }
  service_map:
    - { service: "cl-1-lighthouse-geth", role: cl, client: lighthouse, image: "sigp/lighthouse:v8.1", paired: ["el-1-geth-lighthouse"] }
  setup_summary: { fork_schedule: { gloas: 8 }, blob_schedule: {}, load: [], builders: ["buildoor"] }
  observations:
    consensus:
      # source is the tool/datasource; the observed service goes in its own label
      # (evidence item shape: runbooks://devnet_issue_contract)
      - { service: "cl-1-lighthouse-geth", role: cl, source: beacon-api, ref: "GET /eth/v1/beacon/states/head/finality_checkpoints", at: "epoch 13", detail: "finalized epoch 12, unchanged for 2 epochs" }
    execution: []
    validators_builders: []
    network: []
    tooling: []
  handles:                # collation copies these into each issue's handles block
    network_target: { kind: compute, sandbox_id: "sbx-4", enclave: "devnet-1" }
                          # local: { kind: local, enclave: "devnet-1" }
                          # public: { kind: public, network_id: "peerdas-devnet-6" }
    final_snapshot_id: "" # compute targets only — local and public
                          # targets keep the key with ""; capture exactly one after
                          # end_epoch (runbooks://panda_compute_kurtosis_lifecycle)
```

## Self-Check

Before returning:
- The network target was resolved explicitly (enclave selected, or public network id
  resolved to a concrete active member) and client labels verified against runtime
  evidence.
- The watch window is identical for all observers.
- Observations queried only the target network's own datasources/endpoints.
- Empty filtered logs are described as absent from the fetched tail, not globally absent.
- Output stays neutral; judgment is left to `runbooks://devnet_issue_collation`.
