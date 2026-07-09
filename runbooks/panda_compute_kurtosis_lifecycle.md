---
name: Run a Devnet on Panda Compute
description: Provision a panda-compute sandbox, run an ethereum-package devnet inside it, capture epoch-aligned snapshots, and restore them — the remote-compute provider with snapshot/restore, emitting a compute network_target. Use for devnets that need snapshotting or must run off the local machine.
tags: [panda-compute, kurtosis, sandbox, snapshot, restore, devnet]
triggers:
  - run a devnet on remote compute
  - snapshot a devnet at an epoch and restore it later
  - provision a panda compute sandbox with kurtosis
  - restore a broken devnet state from a snapshot
prerequisites: [compute]
---

Owns the remote-compute devnet lifecycle: provision, launch, snapshot, restore. Emits a
`network_target` (`kind: compute`). Config synthesis comes from
`runbooks://kurtosis_devnet_config`; enclave access and service discovery follow
`runbooks://kurtosis_devnet` once the sandbox is up. For a devnet with no compute, use
a local enclave instead (`runbooks://kurtosis_devnet`).

## Inputs
Required: a compute template (and, to launch, an args file), or a snapshot id to restore.
Preferred: a TTL, and the target snapshot epochs for a capture run.

## Output
A `network_target` (`kind: compute`) plus the lifecycle summary below — enough
metadata for a later watch or investigation to restore the exact state.

## Capability check

Record the surface before mutating remote state: `panda version`,
`panda compute --help`, `panda compute datasources`, `panda compute images list`.
If compute is unavailable, stop with a clear capability error — the caller decides
whether a local run is an acceptable substitute. Unavailable includes auth failures:
an advertised compute datasource with `images list` returning 401/invalid-token
means compute exists but is unusable until credentials are fixed — a successful
image listing, not the datasource advertisement, is the go signal.

## Async operation rule

Most mutations return an operation id. Poll `panda compute operations get <op_id>` to a
terminal state, THEN resolve the concrete sandbox/snapshot id from the result or a
list/get. Record op ids, terminal states, and created ids as you go.

## Clock and boot flavor

`images list` reports a CLOCK per named image (template): `frozen` or `realtime`.
Snapshot/restore is only coherent on a **frozen-clock** template — its guest clock does
not track wall-clock, so a devnet does not skip epochs across a stop/snapshot/restore
gap and a restored VM resumes without drift. A `realtime` template keeps advancing
against wall-clock: restore it after any real gap and the consensus layer is far
behind, attestations flood, and finality may never recover. Use a frozen template
(`ethereum-devnet`, `kurtosis-warm`) for any capture-and-restore run; reject a realtime
one for this purpose.

Always treat the sandbox clock as independent of wall-clock and check it before you
rely on timing. A frozen template in particular boots at a stale baked-in time (it can
be hours or a day behind wall-clock): run `date -u` inside the sandbox first, and if
launch or epoch math depends on wall-clock, set it explicitly
(`sandboxes exec <id> -- date -u -s "<UTC>"`) before `kurtosis run`. Genesis time and
every epoch target time are in the SANDBOX clock frame, not the orchestrator's — carry
them forward as such.

Restore boots a sandbox from a snapshot with `sandboxes create --snapshot`. Keep the
default `--boot-flavor warm`: it resumes the snapshot's memory so the network continues
in its exact in-flight state — same genesis time, same peers. `--boot-flavor cold` does
a fresh boot on the snapshot disk instead (OS and clients restart and resync) — a
reboot, not a continuation; use it only when a cold start is what you want.

## Teardown

- **Stop/start** (`sandboxes stop <id>` … `sandboxes start <id>`) keeps the same
  sandbox and its id for a later restart; **pause/resume** only parks the vCPUs.
- **Delete** (`sandboxes delete <id>`) releases the sandbox entirely. Its snapshots
  survive independently and stay restorable into new sandboxes, so when a run captures a
  snapshot and then hands off through that snapshot, delete the source sandbox — the
  snapshot is the durable artifact, not the sandbox. Deleting still requires an explicit
  caller request (see Safety).

## Lifecycle

- **Provision:** `panda compute sandboxes create --template <t> --ttl <ttl>` → poll →
  `sandboxes get <id>` for connection details → confirm Docker + Kurtosis present.
  When the caller separated provision from deployment, stop after provisioning.
- **Launch (sandbox-side):** drive everything inside the sandbox with
  `sandboxes exec <id> -- …`. Copy the args file in, then `kurtosis run --args-file
  <config> <package_ref>`. Image pulls can take several minutes and outlast a short exec
  timeout, so launch detached (`nohup kurtosis run … &`) and poll the log to completion;
  on a retry, first clear a stale enclave and its leftover docker network / logs-collector
  container (`kurtosis enclave rm -f <name>`, then `docker network rm kt-<name>`) or the
  rerun collides. Record package ref, args path, enclave, genesis time, and
  blocks-produced. Discover services via `runbooks://kurtosis_devnet`.
- **Epoch-aligned snapshots:** snapshot for epoch N at the START of epoch N. Compute
  `target_time = genesis_time + N * slots_per_epoch * seconds_per_slot` before launch,
  and use the SANDBOX clock (restored VMs may drift from wall-clock). Snapshot from the
  orchestrator side only: `panda compute sandboxes snapshot <id> --note "…"` → poll →
  resolve id. Preserve target-epoch order.
- **Restore:** `panda compute sandboxes create --snapshot <snapshot_id> --ttl <ttl>`
  (keep the default `--boot-flavor warm`) → poll → a NEW sandbox id (unrelated to the
  original). Genesis time is preserved, so epoch target times still compute from it on
  the sandbox clock. Then establish enclave, current epoch/slot, slot timing, service
  inventory, and setup summary.
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
  network_target: { kind: compute, sandbox_id: "sbx-4", enclave: "devnet-1" }
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
