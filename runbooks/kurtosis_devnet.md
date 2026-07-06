---
name: Run and Reach a Kurtosis Devnet
description: Start an Ethereum devnet locally with Kurtosis (ethereum-package) or attach to a running enclave, then map its CL/EL/VC/tooling services, resolve endpoints by port name, verify the rendered config, and read its logs and OTel data correctly — local OTel logs live in the local-kurtosis datasource (db otel, table otel_logs, always filter by EnclaveName). Use to launch a local devnet, reach an existing or restored enclave, or pull service logs without the tail-before-filter trap.
tags: [kurtosis, devnet, enclave, local, logs, otel]
triggers:
  - run a devnet locally with kurtosis
  - kurtosis enclave services endpoints ports
  - read kurtosis service logs or otel logs for an enclave
  - query otel_logs by EnclaveName on local-kurtosis
  - attach to a running local enclave
  - verify devnet config fork epochs after launch
---

Owns starting a local Kurtosis devnet and reaching any Kurtosis enclave (local or
restored): service discovery, endpoints, config verification, and log access. Emits a
`network_target` with `kind: local-enclave` — see `runbooks://debug_ethereum_network`
for how targets are consumed. Snapshot and restore live on remote compute only
(`runbooks://panda_compute_kurtosis_lifecycle`); a local devnet is observed live or its
evidence preserved as historical.

## Inputs
Required: an ethereum-package args file (`runbooks://kurtosis_devnet_config`), or the
name of an already-running enclave to attach to.
Preferred: the package ref (default `github.com/ethpandaops/ethereum-package`).

## Output
A reachable enclave: name, service map, verified rendered config, and working log
access — as a `network_target` when a downstream step consumes it:

```yaml
network_target: { kind: local-enclave, enclave: "devnet-1" }
```

## Start or attach

- **Start fresh:** `kurtosis run --args-file <config> <package_ref>`; capture the
  enclave name and genesis time, and confirm it is producing blocks.
- **Attach:** the caller MUST name the enclave; picking one from the list is guessing —
  if ambiguous, stop and ask. Discover with `kurtosis enclave ls`, then
  `kurtosis enclave inspect <enclave>` — its output includes the full service table
  with port mappings (current CLIs have no `service ls`).

## Map services

Naming convention (a HINT — confirm against runtime): CL `cl-{i}-{cl}-{el}`, EL
`el-{i}-{el}-{cl}`, VC `vc-*`. Some clients embed the validator in the beacon process
(no `vc-*`). Confirm client and role via `/eth/v1/node/version`, `web3_clientVersion`,
image tag, or log samples before applying a label. Resolve endpoints by Kurtosis
**port name**, never a fixed localhost port.

## Verify the rendered config

After launch or restore, verify the RUNNING config, not just the input args file:
`<beacon>/eth/v1/config/spec` (`PRESET_BASE`, `SECONDS_PER_SLOT`, `SLOTS_PER_EPOCH`,
fork epochs, `BLOB_SCHEDULE`) and EL `eth_chainId`. A successful `--dry-run` proves
args parse — not live health, image behavior, or post-fork liveness.

## Read logs

- **Prefer OTel-in-ClickHouse when present.** Panda exposes the enclave's OTel
  ClickHouse as the `local-kurtosis` datasource (db `otel`, table `otel_logs`). One
  local ClickHouse holds multiple devnets — `SELECT DISTINCT EnclaveName` first, then
  always filter by `EnclaveName`. The collector often leaves
  `SeverityText`/`SeverityNumber` empty, so triage severity with
  `match(Body, '(?i)(crit|err|error|fatal)')`. Local enclave logs live here — the
  hosted `clickhouse-raw`/`clickhouse-refined` clusters carry hosted networks only.
  Run it like any other datasource — `panda clickhouse query local-kurtosis "<sql>"`
  in a terminal, or `clickhouse.query("local-kurtosis", "<sql>")` in a Python sandbox;
  full query rules in `runbooks://clickhouse_querying`.
- **Otherwise `kurtosis service logs <enclave> <service>`.** LOG-TAIL GOTCHA: Kurtosis
  applies `-n` (tail) BEFORE `--match`/`--regex-match`, so a small tail hides older
  matches. Use `-n 2000`–`5000` or `-a` for small logs, and treat an empty filtered
  result as "not present in the fetched tail," NOT proof the pattern never occurred.
- If a service has no logs, verify it directly via its API before concluding it is
  down — a node can be healthy but simply not shipping logs.

## Useful CL endpoints

`/eth/v1/node/syncing`, `/eth/v1/node/peer_count`, `/eth/v1/beacon/headers/head`,
`/eth/v1/beacon/states/head/finality_checkpoints`, `/eth/v1/config/spec` — spell the
full `/eth/v1/...` path; shorthand forms like `/config/spec` return 404.
