---
name: Run a Devnet on Panda Compute
description: Provision a panda-compute sandbox, run an ethereum-package devnet inside it, capture epoch-aligned snapshots, and restore them — the remote-compute provider with snapshot/restore, emitting a compute-enclave network_target. Use for devnets that need snapshotting or must run off the local machine.
tags: [panda-compute, kurtosis, sandbox, snapshot, restore, devnet]
triggers:
  - run a devnet on remote compute
  - snapshot a devnet at an epoch and restore it later
  - provision a panda compute sandbox with kurtosis
  - restore a broken devnet state from a snapshot
prerequisites: [compute]
---

Owns the remote-compute devnet lifecycle: provision, launch, snapshot, restore. Emits a
`network_target` (`kind: compute-enclave`). Config synthesis comes from
`runbooks://kurtosis_devnet_config`; enclave access and service discovery follow
`runbooks://kurtosis_devnet` once the sandbox is up. For a devnet with no compute, use
a local enclave instead (`runbooks://kurtosis_devnet`).

## Inputs
Required: a compute template (and, to launch, an args file), or a snapshot id to restore.
Preferred: a TTL, and the target snapshot epochs for a capture run.

## Output
A `network_target` (`kind: compute-enclave`) plus the lifecycle summary below — enough
metadata for a later watch or investigation to restore the exact state.

## Capability check

Record the surface before mutating remote state: `panda version`,
`panda compute --help`, `panda compute datasources`, `panda compute templates list`.
If compute is unavailable, stop with a clear capability error — the caller decides
whether a local run is an acceptable substitute.

## Async operation rule

Most mutations return an operation id. Poll `panda compute operations get <op_id>` to a
terminal state, THEN resolve the concrete sandbox/snapshot id from the result or a
list/get. Record op ids, terminal states, and created ids as you go.

## Lifecycle

- **Provision:** `panda compute sandboxes create --template <t> --ttl <ttl>` → poll →
  `sandboxes get <id>` for connection details → confirm Docker + Kurtosis present.
  When the caller separated provision from deployment, stop after provisioning.
- **Launch:** copy the args file in, `kurtosis run --args-file <config> <package_ref>`;
  record package ref, args path, enclave, genesis time, and blocks-produced. Discover
  services via `runbooks://kurtosis_devnet`.
- **Epoch-aligned snapshots:** snapshot for epoch N at the START of epoch N. Compute
  `target_time = genesis_time + N * slots_per_epoch * seconds_per_slot` before launch,
  and use the SANDBOX clock (restored VMs may drift from wall-clock). Snapshot from the
  orchestrator side only: `panda compute sandboxes snapshot <id> --note "…"` → poll →
  resolve id. Preserve target-epoch order.
- **Restore:** `panda compute snapshots restore <snapshot_id> --ttl <ttl>` → poll → a
  NEW sandbox id (unrelated to the original). Then establish enclave, current
  epoch/slot, slot timing, service inventory, and setup summary.
- **Final snapshot (watch runs):** exactly one after the end epoch is reached; poll and
  resolve before any downstream judgment.

## Safety

Set TTLs on everything; use idempotency keys for retryable mutations when supported;
snapshot from the orchestrator side only; deleting sandboxes or snapshots requires an
explicit caller request.

## Output shape

```yaml
lifecycle:
  summary: >
    Provisioned sbx-4 from template kurtosis-xl (ttl 4h), launched peerdas smoke
    enclave devnet-1 (genesis 10:02:11Z), captured snapshots at epochs 3 and 5,
    final snapshot snap-9 after end epoch 14.
  network_target: { kind: compute-enclave, sandbox_id: "sbx-4", enclave: "devnet-1" }
  sandbox: { id: "sbx-4", template: "kurtosis-xl", ttl: "4h" }
  launch: { package_ref: "github.com/ethpandaops/ethereum-package", args_file: "./local.yaml", genesis_time: "2026-07-01T10:02:11Z", blocks_produced: true }
  snapshots: [ { epoch: 3, snapshot_id: "snap-7", operation_id: "op-31" }, { epoch: 5, snapshot_id: "snap-8", operation_id: "op-38" } ]
  restore: { attempted: false, source_snapshot_id: "", new_sandbox_id: "" }
  warnings: []
  citations: ["panda compute operations get op-31"]
```

## Self-Check

Before returning:
- Every mutation has an operation id (or an explicit reason it lacks one), polled to a
  terminal state before its resource id was recorded.
- Snapshots are epoch-aligned on the sandbox clock and network slot timing.
- Restore output uses the NEW sandbox id.
- The summary carries enough to restore the exact state later.
