---
name: Watch a Devnet and Record Facts
description: Observe a running Kurtosis Ethereum devnet (local or restored) over a fixed epoch window and collect neutral per-node and network-wide observations — consensus, execution, validator/builder, and tooling lanes — as facts for a later collation step to judge. Use to watch a devnet without deciding what is or isn't a problem.
tags: [devnet, watch, observability, kurtosis, consensus, execution]
triggers:
  - watch a devnet for a few epochs
  - collect observations from a running enclave
  - monitor devnet nodes without judging issues
---

Owns neutral observation of a running devnet over a watch window. Watchers report
FACTS; judging belongs to `runbooks://devnet_issue_collation`. For interactive
debugging use `runbooks://debug_ethereum_network` instead.

## Inputs
Required: a `network_target` of kind `local-enclave` or `compute-enclave`, and a watch
length in epochs. Hosted networks are not watched with this runbook — they lack the
enclave service/log access it depends on; observe them live via
`runbooks://debug_ethereum_network` instead.
Preferred: the setup (clients, tooling, load) for context.

## Output
Neutral observations per lane plus a service map and setup summary (shape below).
Verdicts are out of scope — record what happened, not whether it is a problem, and
leave root-causing to downstream stages.

## Setup

- Reach and map the enclave, resolve ports, read logs, and verify the rendered config
  with `runbooks://kurtosis_devnet`. Keep validator/builder/tooling services as their
  own observation lane — a devnet can finalize while the builder path is degraded
  (`runbooks://ethereum_protocol_model`).
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

Use bounded log pulls. Kurtosis applies `-n` (tail) BEFORE `--match`, so pull
`-n 2000`+ and treat an empty filtered result as absent from the fetched tail, not
globally absent (owner: `runbooks://kurtosis_devnet`).

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
    network_target: { kind: compute-enclave, sandbox_id: "sbx-4", enclave: "devnet-1" }
    final_snapshot_id: "" # compute-enclave targets only; capture exactly one after
                          # end_epoch (runbooks://panda_compute_kurtosis_lifecycle)
```

## Self-Check

Before returning:
- The enclave was selected explicitly and client labels verified against runtime evidence.
- The watch window is identical for all observers.
- Local observations queried local datasources only.
- Empty filtered logs are described as absent from the fetched tail, not globally absent.
- Output stays neutral; judgment is left to `runbooks://devnet_issue_collation`.
