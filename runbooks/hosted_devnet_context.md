---
name: Ground a Hosted Devnet's Context
description: Resolve a hosted ethpandaops devnet into authoritative metadata — network id from networks://active and the networks://<network> resource, node inventory from node_inventory_url, fork and blob schedule, slot timing, endpoints, participant images (panda devnets info|endpoints|forks|clients) — before config generation or incident triage. Emits a hosted network_target and a grounded context summary.
tags: [devnet, hosted, network, inventory, forks, context]
triggers:
  - which devnet is currently active resolve network id
  - node inventory client pairs images for a hosted devnet
  - fork schedule slot timing endpoints of a devnet
  - read networks://active or node_inventory_url for a devnet
  - ground devnet context before reproducing it locally
---

Owns grounding a hosted devnet into authoritative context. Emits a `network_target`
(`kind: hosted`) plus the context summary below. Config synthesis builds on this
(`runbooks://kurtosis_devnet_config`); judging behavior stays with
`runbooks://debug_ethereum_network`.

## Inputs
Required: a concrete network id, or a devnet group to resolve. A display name like
`devnet-6` is NOT globally unique — resolve the active member from `networks://active`;
an ambiguous name stops here and surfaces the candidates.

## Output
A hosted `network_target` and the context summary below, with `context_complete: false`
whenever a field the caller's next step needs is unresolved.

## Source authority

| Source | Use it for | Caveat |
| --- | --- | --- |
| `networks://active` | Active ids and devnet groups | Display names may be duplicated |
| `networks://<network>` | Repo/path, chain id, genesis artifact URLs, service URLs, fork/blob schedule, spec notes, participant images, `node_inventory_url` | Spec notes can be aspirational |
| `node_inventory_url` | Deployed labels, roles, client pairs, host composition | May not describe protocol constants |
| CL config (`.../cl/config.yaml`) | Slot timing, fork epochs, presets | Prefer over memory/defaults |
| Package schema (`network_params.yaml`) | Valid ethereum-package args fields | Package versions move |

For deployed topology/images, live inventory and artifacts beat notes; for protocol
intent, start from the network resource's fork/EIP links, then verify with specs.
Report source disagreements rather than silently choosing
(`runbooks://evidence_discipline`).

## Missing-input behavior

- No concrete network id → stop and surface candidates.
- Missing `node_inventory_url` → continue only if another source identifies
  participants; mark topology confidence low.
- CL config unreadable → mark incomplete rather than inferring slot timing/fork epochs
  from defaults.
- Endpoints missing but inventory/artifacts present → config generation may continue;
  hosted-live debugging stops.

## Intake

1. Resolve the network id: read the `networks://active` resource;
   `panda devnets -o json` (use `active_devnet_groups` to pick the active member).
2. Read the network resource: `panda devnets info|endpoints|forks|clients <network> -o json`.
3. Fetch the node inventory when present — record RAW inventory, not just a summary
   (labels are how later logs, validator ranges, builders, and client pairs get mapped).
4. Fetch CL config (slot timing, fork epochs, validator counts) and EL
   genesis/chainspec from the genesis artifact host when they affect the task.
5. Build a short active protocol model (`runbooks://ethereum_protocol_model`); search
   the consensus-specs, eips, and examples indexes rather than guessing.

## Output shape

```yaml
context:
  summary: >
    peerdas-devnet-6 resolved from the active group; 24 nodes across 5 CL / 4 EL
    clients; Fulu at epoch 0, Gloas at 256; 6s slots, 32/epoch. Inventory and CL
    config agree; the network resource's blob note is aspirational (disagreement
    recorded).
  network_target: { kind: hosted, network_id: "peerdas-devnet-6" }
  endpoints: { dora: [], forky: [], beacon_rpc: [], json_rpc: [], prometheus: [] }
  fork_and_timing: { fork_schedule: {}, blob_schedule: {}, seconds_per_slot: 6, slots_per_epoch: 32, chain_id: "", genesis_time: "" }
  topology: { participants: [], client_pairs: [], validator_ranges: [], raw_inventory_ref: "" }
  artifact_urls: { cl_config: "", el_genesis: "", node_inventory: "" }
  source_disagreements: []
  context_complete: true
  citations: ["panda devnets info peerdas-devnet-6 -o json"]
```

## Self-Check

Before returning:
- The network id is concrete, not a display label.
- Deployed topology/images came from inventory or artifacts where available.
- Fork schedule and slot timing came from CL config or the network resource, not memory.
- Disagreements are visible; `context_complete` reflects unresolved fields.
