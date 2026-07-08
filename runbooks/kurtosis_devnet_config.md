---
name: Build a Kurtosis Config From a Public Devnet
description: Generate one runnable ethereum-package args file that reproduces a public (hosted) ethpandaops devnet locally — from grounded inventory, the package schema, and optional steering. The primary output is the args file itself, ready for kurtosis run.
tags: [kurtosis, ethereum-package, config, args-file, reproduction]
triggers:
  - reproduce a public devnet locally with kurtosis
  - generate an ethereum-package args file
  - local kurtosis config matching a devnet's forks clients images
---

Owns synthesis of a runnable ethereum-package args file that models a public devnet
locally. Consumes the Output of `runbooks://public_devnet_context` (network id,
inventory, fork/blob schedule, images); the file it emits feeds
`runbooks://kurtosis_devnet` or `runbooks://panda_compute_kurtosis_lifecycle`.

## Inputs
Required: grounded public-devnet context (`runbooks://public_devnet_context`).
Preferred: a package ref (default `github.com/ethpandaops/ethereum-package`) and any
steering (client matrix, node count, fork epochs, load).

## Output
One runnable args file plus the synthesis summary below — complete only when it can be
handed directly to `kurtosis run --args-file`. Emit the file, not a prose plan.

## Procedure

1. **Package + schema.** Use the caller's package ref or the default; inspect
   `network_params.yaml` for valid fields before writing YAML — the package schema
   owns valid keys, so every key you emit was checked against it or an example.
   `network_params.yaml` can lag real keys (e.g. `preset`); when a key you need is
   absent, check the package's schema/sanity-check code or a shipped example before
   concluding it is unsupported.
2. **Topology.** Without steering, build a minimal faithful slice: the deployed fork
   schedule + slot timing, ≥1 representative CL/EL pair, preserved client/image
   versions where the package supports explicit images, only necessary tooling. With
   steering, adjust the client matrix / node count / validator distribution / fork
   epochs / services / load, recording every deviation — steering is a reproduction
   request, not evidence about the public network.
3. **Params from sources** (`runbooks://ethereum_protocol_model` for what matters per
   fork): chain id, genesis timing, seconds/slot, slots/epoch, fork epochs, blob
   schedule + limits, validator counts.
4. **Clients + images** from inventory; if a field can't express a deployed image
   exactly, use the closest mechanism and record it. Flag a stray image tag that
   conflicts with deployed inventory instead of copying it.
5. **Tooling** via supported `additional_services`/params: Dora, Prometheus (when a
   follow-up watch compares nodes), Buildoor (when ePBS is in scope),
   Spamoor/tx-fuzz/blobber (only when throughput/blobs must be exercised). Client-flag
   caveat: Besu `--miner-extra-data` expects hex bytes; Geth `--miner.extradata`
   accepts a plain string.
6. **Validate:** `--dry-run` proves the args parse and render — record it as exactly
   that, and read the rendered plan for Starlark errors rather than trusting the exit
   code alone. A dry-run still creates an (empty) enclave; remove it so the real
   launch can reuse the name. Live health, fork activation, and builder behavior are
   verified after boot via `runbooks://kurtosis_devnet`.

### Accelerated Gloas/ePBS smoke

Preserve the deployed chain id + selected images; use the `minimal` preset only as an
explicit local acceleration (recorded as a deviation, not a faithful clone — and
expect some client images to lack a minimal-preset build: a participant that fails to
parse the rendered config is an image/preset mismatch; drop or swap it); keep BPO
changes before Gloas for a clean smoke (BPO epochs 1 and 2 → local Gloas at epoch ≥3
unless deliberately stressing a stacked boundary — fork/BPO boundary semantics:
`runbooks://ethereum_protocol_model`); with Fulu active and a small validator set,
give at least one participant full custody (`supernode: true`) — PeerDAS validation
fails in tiny topologies without one; include Buildoor when observing the builder path.

## Minimal args-file

The load-bearing shape is small — one or two participant pairs, network params, and the
tooling you actually need. Confirm exact keys against the package's `network_params.yaml`
and a shipped example; do NOT reconstruct the schema by reading the package's Starlark
source.

```yaml   # shape to ADAPT — swap images, epochs, and counts for the deployed devnet
participants:
  - el_type: geth
    el_image: ethpandaops/geth:<devnet-tag>
    cl_type: lighthouse
    cl_image: ethpandaops/lighthouse:<devnet-tag>
    use_separate_vc: true
    vc_type: lighthouse
    validator_count: 64
    supernode: true          # ≥1 full-custody node keeps a tiny PeerDAS topology viable
  - el_type: nethermind
    el_image: ethpandaops/nethermind:<devnet-tag>
    cl_type: teku
    cl_image: ethpandaops/teku:<devnet-tag>
    validator_count: 64
network_params:
  preset: mainnet            # `minimal` only as a recorded acceleration deviation
  seconds_per_slot: 12
  genesis_delay: 60          # short, so the network reaches genesis promptly
  # fork activation epochs + blob (BPO) schedule copied from the deployed config, e.g.:
  # fulu_fork_epoch: 0
additional_services:
  - dora                     # add per-participant buildoor when ePBS is in scope
```

## Output shape

```yaml
config_synthesis:
  summary: >
    Faithful 2-pair slice of peerdas-devnet-6: lighthouse/geth + teku/besu at deployed
    images, Fulu at 0, Gloas moved 256 -> 3 (steering: accelerated smoke), Dora +
    Buildoor enabled. Dry-run passed.
  args_file: "./peerdas-devnet-6-local.yaml"
  package_ref: "github.com/ethpandaops/ethereum-package"
  topology: { cl: [lighthouse, teku], el: [geth, besu], validators: 128, tooling: [dora, buildoor] }
  deviations: ["gloas epoch 256 -> 3 (accelerated smoke, caller steering)"]
  source_disagreements: []
  dry_run: { attempted: true, command: "kurtosis run --dry-run ...", passed: true }
  citations: ["node inventory (node_inventory_url) for client pairs + images", "panda devnets forks peerdas-devnet-6 -o json"]
```

## Self-Check

Before returning:
- The output includes a runnable args-file path or artifact reference.
- Every YAML key was checked against the package schema or an example.
- Deployed facts and steering deviations are separated; disagreements preserved.
- Dry-run status is explicit, including unavailable/failed states.
