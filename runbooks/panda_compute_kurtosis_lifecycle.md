---
name: Run a Devnet on Panda Compute
description: Provision a panda-compute sandbox, run an ethereum-package devnet inside it, capture epoch-aligned snapshots, and restore them — the remote-compute provider with snapshot/restore, emitting a compute network_target. Use for devnets that need snapshotting or must run off the local machine.
tags: [panda-compute, kurtosis, sandbox, snapshot, restore, devnet]
triggers:
  - run a devnet on remote compute
  - snapshot a devnet at an epoch and restore it later
  - provision a panda compute sandbox with kurtosis
  - restore a broken devnet state from a snapshot
  - how much cpu ram disk for a compute sandbox running kurtosis
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

All waiting happens INSIDE your current agent turn: keep issuing poll calls (bounded
`until …; do sleep 15; done` shell loops are fine) until the state you need arrives.
For a known-ETA wait (an epoch target time), sleep the computed remainder in one
bounded call and confirm with a single poll — reserve the short-interval loop for
unknown durations (operations, image pulls). Never end your turn to wait on a timer
or a background watcher — in workflow-worker harnesses, ending the turn signals task
completion and your output is collected immediately.

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
default warm flavor by **omitting the `--boot-flavor` flag entirely**: warm is the
server default, and some deployed backends reject the field with
`HTTP 400: json: unknown field "flavor"` even though the CLI help advertises it.
Warm resumes the snapshot's memory so the network continues in its exact in-flight
state — same genesis time, same peers. A cold boot (fresh boot on the snapshot disk;
OS and clients restart and resync) is a reboot, not a continuation. When a cold boot
is genuinely what you want, attempt `--boot-flavor cold`: the 400 is a request-decode
rejection that creates nothing, so a failed attempt is free — treat it as "this
deployment cannot cold-boot" and say so, rather than silently substituting a warm
restore.

## Restore capacity and contention

- **A warm restore makes every guest page resident.** A restored 64 GiB sandbox
  occupies a real 64 GiB of host memory regardless of what the guest is doing, so
  host-side usage metrics (`metrics.memUsedGiB`, `kubectl top`) read ~100% on every
  restored sandbox — that is residency, not pressure. But node capacity is genuinely
  consumed: only the supervisor's small fixed allowance sits above guest size, and
  restores have been OOMKilled (`exit code 137`) when it ran out.
- **A freshly captured snapshot is not immediately restorable.** Restore waits on
  retained snapshot zvol capacity / archive upload; a create that fails or sits in
  `placement deferred: … waiting for retained snapshot zvol capacity or archive
  upload` will not clear within a poll loop's horizon — fail fast and fall back
  (live target, existing sandbox) instead of burning the timeout.
- **Fan-out siblings must not stampede.** Before `sandboxes create --snapshot`, run
  `sandboxes list -o json` and reuse a running sandbox whose lineage
  (`sourceSnapshot`, name) covers your restore point; at most one restore per
  snapshot per run.
- **Shared-sandbox etiquette.** Ownership follows the TASK, not the process that ran
  the create: a sandbox provisioned on a task's behalf — e.g. an orchestrator or
  lifecycle step restoring a snapshot so a watch task can run on it — belongs to that
  task, and that task's worker may snapshot it. A shared sandbox is one another live
  task created for its own purposes and is still using. When reusing a shared
  sandbox: never stop, snapshot, or delete it; record the dependency in your output;
  treat its `expiresAt` TTL as a hard deadline for any watch recipe. The Teardown
  delete-the-source rule below applies only to sandboxes your task owns.

## Sizing

Template defaults are small — `ethereum-devnet` and `kurtosis-warm` provision
4 vCPU · 4 GiB RAM · 8 GiB disk (`images list -o json`, `template.sizing`) — enough
for only the smallest smoke enclave. Size the sandbox to the kurtosis config at
create time with `--vcpu`, `--memory-mb`, and `--disk-gb` on `sandboxes create`:
the server honors overrides beyond the template's advertised `vcpuOptions`, capped
by the `limits` in `panda compute meta`. There is no resize command, and a snapshot
is not an escape hatch — snapshot-sourced creates resolve through warm restore,
which rejects a memory override that differs from the snapshot
(`override_not_allowed`) and requires vCPU ≥ the snapshot's value — so the only fix
for an undersized sandbox is a fresh template create plus full redeploy. Budget
before launch: as a rule of thumb, ~1 vCPU and ~2 GiB RAM per CL/EL participant
pair plus headroom for VCs and tooling (dora, prometheus, spamoor), and ≥20 GiB
disk — client image pulls alone can exhaust the 8 GiB default. Verify from inside
after boot: `sandboxes exec <id> -- sh -c 'nproc; free -m; df -h /'`.

## Teardown

- **Stop/start** (`sandboxes stop <id>` … `sandboxes start <id>`) keeps the same
  sandbox and its id for a later restart; **pause/resume** only parks the vCPUs.
- **Delete** (`sandboxes delete <id>`) releases the sandbox entirely. Its snapshots
  survive independently and stay restorable into new sandboxes, so when a run captures a
  snapshot and then hands off through that snapshot, delete the source sandbox — the
  snapshot is the durable artifact, not the sandbox. Deleting still requires an explicit
  caller request (see Safety).

## Lifecycle

- **Provision:** `panda compute sandboxes create --template <t> --ttl <ttl>` (plus
  `--vcpu/--memory-mb/--disk-gb` sized to the config — see Sizing above) → poll →
  `sandboxes get <id>` for connection details → make the first exec prove the guest
  works AND has the tooling:
  `sandboxes exec <id> -- sh -c 'docker version && kurtosis version'`. An HTTP 500
  from the exec transport itself means a broken exec agent — some images boot to
  `running` but fail every exec this way: the sandbox is
  unusable; delete it (the one deletion Safety pre-authorizes — nothing has landed on
  it yet) and pick a different template, still frozen-clock for any capture-and-restore
  run (see Clock and boot flavor). A clean exec whose command fails means missing
  tooling, not a broken agent. When the caller separated provision from deployment,
  stop after provisioning.
- **Launch (sandbox-side):** drive everything inside the sandbox with
  `sandboxes exec <id> -- …`. Copy the args file in, then `kurtosis run --args-file
  <config> <package_ref>`. Pass `--privileged` whenever the config carries a
  `launch_requirements` entry or might include a privileged service — trigger list
  owned by `runbooks://kurtosis_devnet_config`; inside a disposable compute sandbox
  the flag only permits (never forces) privilege, so pass it whenever unsure. Without
  it the launch dies mid-Starlark with
  `ServiceConfig requested privileged=true, bind_mounts, or host_pid_namespace=true,
  but this run did not opt in. Pass --privileged on the CLI` — AFTER creating the
  enclave, so the retry needs the stale-enclave cleanup below.
  Image pulls can take several minutes and outlast a short exec
  timeout, so launch detached (`nohup kurtosis run … &`) and poll the log to completion;
  on a retry, first clear a stale enclave and its leftover docker network / logs-collector
  container (`kurtosis enclave rm -f <name>`, then `docker network rm kt-<name>`) or the
  rerun collides. Record package ref, args path, enclave, genesis time, and
  blocks-produced. Discover services via `runbooks://kurtosis_devnet`.
- **Epoch-aligned snapshots:** snapshot for epoch N at the START of epoch N. Compute
  `target_time = genesis_time + N * slots_per_epoch * seconds_per_slot` before launch,
  and use the SANDBOX clock (restored VMs may drift from wall-clock). Epoch 0 is the
  carve-out: its record is the base snapshot, captured early in epoch 0 once the
  network is producing blocks — genesis itself has no captured state worth keeping,
  and the capture must still land before the first epoch boundary. Record the actual
  capture time on the epoch-0 entry (`captured_at`): it is a MID-epoch capture, so it
  is not a pre-`first_bad` restore point for an issue that begins earlier in epoch 0
  (`runbooks://devnet_issue_root_cause` reads `captured_at` for exactly this). If no
  blocks appear before the boundary, capture once at the boundary and note it in
  `warnings` — the broken pre-block state is itself the evidence, and that one
  capture doubles as the epoch-1 snapshot when epoch 1 is also a target. Snapshot from the
  orchestrator side only: `panda compute sandboxes snapshot <id> --note "…"` → poll →
  resolve id. Preserve target-epoch order.
- **Restore:** `panda compute sandboxes create --snapshot <snapshot_id> --ttl <ttl>`
  (omit `--boot-flavor`; warm is the server default — see Clock and boot flavor) →
  poll → a NEW sandbox id (unrelated to the original). Verify the resume was actually
  warm before relying on continuation: client containers inside the enclave show
  `docker ps` uptimes spanning the snapshot — freshly restarted containers mean a
  cold reboot and a resyncing network, so record it as one. Genesis time is
  preserved, so epoch target times still compute from it on the sandbox clock. Then
  establish enclave, current epoch/slot, slot timing, service inventory, and setup
  summary.
- **Final snapshot (on request):** when the caller asks for a final capture at the end
  of an observation window — a watch run on a sandbox it owns is a standing request
  (`runbooks://devnet_watch`) — take exactly one after the end epoch is reached; poll
  and resolve before any downstream judgment. Shared-sandbox etiquette still wins: a
  worker never snapshots a sandbox another live task is using — but a sandbox
  provisioned on this task's behalf is this task's own (see Shared-sandbox
  etiquette). On a genuinely shared sandbox, return the empty id and record the owner
  dependency instead.

## Safety

Set TTLs on everything; use idempotency keys for retryable mutations when supported;
snapshot from the orchestrator side only; deleting sandboxes or snapshots requires an
explicit caller request — the one exception is a just-provisioned sandbox whose exec
agent is broken (see Lifecycle: Provision): nothing has landed on it, so delete and
re-provision without asking.

## Output shape

```yaml
lifecycle:
  summary: >
    Provisioned sbx-4 from template kurtosis-xl (ttl 4h), launched peerdas smoke
    enclave devnet-1 (genesis 10:02:11Z), captured the epoch-0 base snapshot and
    snapshots at epochs 3 and 5, caller-requested final snapshot snap-9 after end
    epoch 14.
  network_target: { kind: compute, sandbox_id: "sbx-4", enclave: "devnet-1" }
  sandbox: { id: "sbx-4", template: "kurtosis-xl", ttl: "4h", sizing: { vcpu: 8, memory_mb: 16384, disk_gb: 40 } }  # as created — restores need vcpu >= and memory == this (see Sizing)
  launch: { package_ref: "github.com/ethpandaops/ethereum-package", args_file: "./local.yaml", genesis_time: "2026-07-01T10:02:11Z", blocks_produced: true }
  snapshots: [ { epoch: 0, snapshot_id: "snap-5", operation_id: "op-28", captured_at: "2026-07-01T10:04:30Z" },  # captured_at required on the epoch-0 base entry — it is a mid-epoch capture
               { epoch: 3, snapshot_id: "snap-7", operation_id: "op-31" }, { epoch: 5, snapshot_id: "snap-8", operation_id: "op-38" } ]
  final_snapshot: { snapshot_id: "snap-9", operation_id: "op-45" }  # only when requested; "" ids otherwise — watch copies snapshot_id into handles.final_snapshot_id
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
