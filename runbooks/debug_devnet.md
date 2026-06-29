---
name: Debug Ethereum Network
description: Debug a hosted Ethereum network by first learning its network metadata, fork/spec context, and available datasources, then using Dora, Forky, Ethnode, and OTel ClickHouse logs to investigate splits, finality delays, missed blocks, ePBS missing payloads, data availability failures, offline nodes, and client bugs.
tags: [network, devnet, debugging, forks, consensus, logs, validators, epbs, peerdas]
prerequisites: [clickhouse-raw]
---

This runbook is for debugging hosted Ethereum networks that Panda can see: multi-node devnets, testnets, and public networks. It is intentionally fork-aware. The investigation MUST first learn the network and active protocol rules, then interpret symptoms under those rules.

This runbook is not for local Kurtosis devnets. Use the local-devnet runbook for local enclaves and local OTel ClickHouse.

The user MUST provide, or the agent MUST resolve, a concrete network id before collecting evidence. Do not guess. Use `networks://active`, `dora.list_networks()`, or `ethnode.list_networks()` to discover candidates. If the user says "this devnet" and there is no unambiguous network in context, ask for the network id.

## Core Principle

Do not start from a fixed mental model such as "missed slot means proposer failed." Ethereum fork rules define which actors, artifacts, and invariants exist. A pre-ePBS network has one slot model; a Gloas/ePBS network has separate beacon-block, payload-reveal, and PTC verdicts; a PeerDAS/Fulu network adds data-column availability. Forks may add new artifacts.

The required pattern is:

1. Discover the network and available evidence sources.
2. Read the network resource and ingest the fork/spec context.
3. Build an active protocol model for the network.
4. Establish a health baseline.
5. Classify symptoms under the active protocol model.
6. Drill into nodes, logs, direct RPC, and code/spec evidence only after classification.
7. Co-relate symptoms and data collected from the live network to find the cause of the failures

## Source Authority

Different Panda sources answer different questions. Treat disagreement as evidence, not noise.

| Source | Use it for | Do not use it for |
| --- | --- | --- |
| `networks://<network>` | Network metadata, service URLs, fork schedule, spec notes, EIP/release links, participant images, node inventory URL | Live node health by itself |
| `node_inventory_url` | Authoritative instance labels and node composition when available | Chain status or finality |
| Dora | Indexed chain view, epochs, slots, validators, missed proposers, offline attesters, links | Direct truth for one node's current view |
| Forky | Fork-choice snapshots and client/node head comparison | Container logs or validator duty attribution |
| Ethnode | Direct beacon/execution RPC against specific instances | Whole-network aggregates unless queried across nodes |
| ClickHouse `external.otel_logs` | Container/runtime behavior and historical logs | Definitive chain truth |
| `search` EIPs/specs/examples/runbooks | Intended protocol behavior and query patterns | Live network state |

## Debug Report

At the start of each debug session, create one report file at `/workspace/<network>-debug-<timestamp>.md`. Append all raw API responses, log extracts, queries, commands, and interpretations to this file as you go. At the end, publish it and provide the user with the URL and local path.

```python
from datetime import datetime
import json
import os
from ethpandaops import storage

network = "<network>"
timestamp = datetime.utcnow().strftime("%Y%m%d-%H%M%S")
debug_file = f"/workspace/{network}-debug-{timestamp}.md"

with open(debug_file, "w") as f:
    f.write(f"# Debug Report: {network}\n")
    f.write(f"**Generated:** {datetime.utcnow().isoformat()}Z\n\n")

with open("/workspace/debug_file_path.txt", "w") as f:
    f.write(debug_file)

def append_debug(title, payload):
    with open("/workspace/debug_file_path.txt") as f:
        debug_file = f.read().strip()
    with open(debug_file, "a") as f:
        f.write(f"\n## {title}\n\n")
        if hasattr(payload, "to_string"):
            f.write(payload.to_string(index=False))
            f.write("\n")
        elif isinstance(payload, str):
            f.write(payload)
            f.write("\n")
        else:
            f.write("```json\n")
            f.write(json.dumps(payload, indent=2, default=str))
            f.write("\n```\n")

def publish_debug_report():
    with open("/workspace/debug_file_path.txt") as f:
        debug_file = f.read().strip()
    remote_name = os.path.basename(debug_file)
    url = storage.upload(debug_file, remote_name=remote_name).url
    with open("/workspace/debug_file_url.txt", "w") as f:
        f.write(url)
    append_debug("Debug Report URL", url)
    return url
```

Every collection step MUST append:

- the exact query, command, API path, or endpoint used
- the raw response or DataFrame before summarizing
- the active timeframe, slot range, or epoch range
- source-specific warnings and data-quality issues
- a short section labeled `Interpretation`

## Citations

A citation is a Panda command that re-derives the cited evidence. Every final finding that names a concrete artifact - node, client, slot, epoch, validator, block root, transaction, builder, or log pattern - MUST include a citation directly under the claim.

Discover the current command surface with `panda --help` and subcommand `--help`; do not hardcode flags from memory.

Use this shape:

```shell
# Fetches the network metadata and fork/spec context
panda resources read networks://<network>
```

If a finding comes from sandbox Python, cite the `panda execute` invocation or include enough code in the report for the user to re-run it.

## Verbatim Output

When reporting label values, instance names, counts, roots, or log lines, paste raw tool output in a fenced code block. Do not paraphrase, reformat, infer, or reconstruct output. If a structured response cannot be pasted as-is, say so explicitly.

If two sources disagree, report the disagreement. Example: Dora may list 16 consensus clients while OTel logs show 30 hosts; Dora is evidence for indexed chain participants, while OTel is evidence for log-shipping hosts.

## Timeframe Rules

Use one active timeframe or slot/epoch window across all related steps unless there is an explicit reason to change it. Record changes in the report.

1. If the user provides a timeframe, slot range, or epoch range, use it.
2. If a split or fork boundary is detected, override the active window to focus around the divergence or fork activation point.
3. If missing payloads are detected, focus on the affected slots plus the next slots that carry PTC verdicts.
4. Otherwise default to the past 1 hour.

## Search-First Rule

Use Panda search to fill gaps instead of guessing APIs, schema, or protocol semantics.

- Examples: `search(type="examples", query="<query pattern you need>")`
- Runbooks: `search(type="runbooks", query="<sub-problem>")`
- EIPs: `search(type="eips", query="<EIP number or topic>")`
- Consensus specs: `search(type="consensus-specs", query="<fork, field, or invariant>")`

Search is especially important near fork boundaries, for new EIPs, and when a slot contains artifacts the older mental model does not explain.

## Phase 0: Network Context Intake

Before interpreting symptoms, gather the network context and available datasource profile.

1. **Resolve the network id** - Read active networks if needed.

   ```shell
   # Lists active network ids known to Panda
   panda resources read networks://active
   ```

2. **Read the network resource** - This is the first required artifact.

   ```shell
   # Fetches network metadata, fork schedule, spec notes, EIPs, releases, service URLs, and inventory URL
   panda resources read networks://<network>
   ```

   Append the full resource to the debug report. Extract and record:

   - network id, repository/path, status, chain id, genesis time
   - service URLs: Dora, Forky, beacon RPC, JSON-RPC, metrics, tracoor, buildoor if present
   - fork schedule and blob schedule
   - `spec.eips`, `spec.releases`, `spec.metrics_url`, `spec.previous_spec_url`
   - participant images and client versions
   - `node_inventory_url`

3. **Read node inventory when available** - Prefer it over deriving names from patterns.

   ```python
   import httpx

   node_inventory_url = "<node_inventory_url from networks://<network>>"
   inventory = httpx.get(node_inventory_url, timeout=40).json()
   append_debug("Node inventory", inventory)
   ```

4. **Discover datasource availability** - Build a data profile, but do not stop just because one source is missing. Use module discovery calls and examples instead of inventing queries:

   - Dora: `dora.list_networks()`
   - Ethnode: `ethnode.list_networks()`
   - Forky: `search(type="examples", query="forky list networks recent frames")`
   - OTel logs: `search(type="examples", query="List devnet nodes shipping logs")`

   Record `has_dora`, `has_ethnode`, `has_forky`, `has_logs`, discovered network lists, OTel hosts, and any errors in the debug report.

   Routing rules:

   - If the network is not present in any live datasource and no network resource exists, report that Panda cannot see the requested network and stop.
   - If Dora is unavailable, use Ethnode/Forky/logs for baseline.
   - If ClickHouse logs are unavailable, continue with Dora/Forky/Ethnode and report that log drilldown is unavailable.
   - If Ethnode is available, use direct RPC to validate hypotheses against specific nodes.

## Phase 1: Protocol and Fork Model

This phase is mandatory. Its output is an "active protocol model" section in the debug report.

1. **Summarize active forks and upcoming boundaries** - Use `networks://<network>` fork schedule first. If Ethnode is available, cross-check with beacon config and fork schedule from a healthy node using `search(type="examples", query="Get chain configuration fork schedule")`.

2. **Search/read relevant specs** - For each EIP or release named by the network resource that could affect the observed symptom, search the relevant corpus.

   Examples:

   ```python
   # Use the search tool/resource exposed by Panda. Prefer exact EIP ids when known.
   search(type="eips", query="EIP-7732")
   search(type="consensus-specs", query="Gloas signed_execution_payload_bid payload_attestations")
   search(type="consensus-specs", query="Fulu data columns blob_data_available")
   search(type="examples", query="debug ethereum network finality split")
   ```

   Record:

   - which fork rules are active now
   - which fork boundary, if any, is near the active timeframe
   - which new actors exist: builder, PTC, data availability committee, sidecar, relay, etc.
   - which new artifacts exist: bid, payload envelope, payload attestation, data column sidecar, execution request, block access list, etc.
   - which invariants must hold for a healthy network

3. **Detect ePBS/Gloas behavior when relevant** - The presence of Gloas in the fork schedule or `signed_execution_payload_bid` in block bodies means the slot model has changed. Use `search(type="examples", query="Detect ePBS and payload-attestation fields")`.

4. **Write the active protocol model** - This should be short and explicit. Example:

   ```text
   Interpretation:
   - Active fork: Gloas at epoch 30.
   - Slot states are not binary. A canonical beacon block can exist without an execution payload.
   - Payload presence is decided by PTC payload_attestations in the next block.
   - Missing payload investigation must identify builder_index and buildoor node before blaming the proposer.
   - Fulu data availability may affect payload reveal/propagation, so blob/data-column availability must be checked for payload-related symptoms.
   ```

## Phase 2: Health Baseline

Build a baseline before drilling into logs. The baseline SHOULD answer: is the network split, finalizing, participating, producing blocks/payloads, and agreeing across clients?

### Dora Baseline

Skip this subsection if `has_dora = false`.

Use Panda examples for exact API patterns:

- `search(type="examples", query="network overview")`
- `search(type="examples", query="network splits")`
- `search(type="examples", query="epoch summary")`
- `search(type="examples", query="missing proposers")`
- `search(type="examples", query="offline attesters")`

Collect and append:

- network overview, including finalized epoch, head slot, current epoch, finalizing status, and data-quality warnings
- Dora `/forks` JSON
- completed epoch summaries across the active timeframe
- missed proposers for the active timeframe
- offline or underperforming attesters
- Dora links for relevant epochs, slots, validators, and blocks

Rules:

- Use the most recent completed epoch for participation. The head epoch is still in progress and may look artificially low.
- If an epoch marked finalized reports participation below 66.7%, flag a source inconsistency instead of concluding the chain finalized below threshold.
- If multiple forks exist, refocus the active window around the divergence slot/epoch.

### Ethnode Baseline

Use this when Ethnode is available, especially when Dora is missing or suspicious.

Use `search(type="examples", query="Compare head roots and finality across nodes")`. Append the raw per-instance rows and explicitly call out whether head roots, finalized checkpoints, and sync status agree.

### Forky Baseline

Use Forky when fork-choice disagreement, reorgs, or client-specific head disagreement is suspected.

- `search(type="examples", query="forky fork choice snapshots")`
- `search(type="examples", query="forky compare head across clients")`
- `search(type="examples", query="forky reorg")`

Record frame ids, nodes, clients, head roots, justified/finalized checkpoints, and Forky links.

### Baseline Summary

Before logs, append a narrative summary:

- Is the network on one fork or split?
- Is finality advancing? How many epochs behind?
- What is the best participation evidence and source?
- Are missed beacon blocks present?
- On ePBS networks, are canonical blocks missing execution payloads?
- On DA/blob networks, are data sidecars/columns available?
- Which nodes, validators, clients, or builders are implicated?
- Which sources disagree?

If the baseline is healthy but the user reports a problem, show the healthy baseline and ask for the observed symptom before doing broad log scraping.

## Phase 3: Symptom Classification

Classify first, then investigate the matching branch. Multiple branches may apply.

| Symptom | Primary branch | First evidence to collect |
| --- | --- | --- |
| Multiple heads/roots, Dora forks, Forky disagreement | Network split / fork choice | Dora forks, Forky frames, Ethnode head roots |
| Finalized checkpoint not advancing | Finality / participation | Ethnode checkpoints, Dora epochs, offline attesters |
| `status=Missing` slots | Missed beacon blocks | proposer duties, proposer node logs, sync/peer status |
| Canonical block exists but no payload on ePBS | ePBS missing payload | PTC verdict, `builder_index`, buildoor logs |
| Payload or block validation errors | EL / Engine API / spec mismatch | CL engine logs, EL logs, block payload details, EIP/spec search |
| Missing blobs, low blob/data availability, column warnings | DA / Blob / PeerDAS | block sidecars, PTC `blob_data_available`, data-column logs |
| One node stuck/offline | Node drilldown | Ethnode sync/peers, node logs, host/container map |
| One client type failing | Client-specific bug | grouped logs by client, versions/images, fork/spec context |

## Branch A: Network Split / Fork Choice

Use this branch when Dora reports multiple forks, Forky shows competing roots, or direct node RPC disagrees on head roots/checkpoints.

1. Identify the earliest divergence slot/epoch and make it the active window.
2. Compare head roots and finalized checkpoints across clients and nodes.
3. Use Forky frames around the divergence to identify which clients/nodes saw which roots.
4. Inspect the first divergent block: proposer, parent root, execution payload or bid, blobs/data columns, and any fork-specific fields.
5. Search EIPs/specs for the active fork feature touched by the divergent block.
6. Compare logs from nodes on different sides of the split around the divergence.

Do not treat minority-fork nodes as simply "offline." On a split, Dora participation and proposer views may reflect the canonical fork only.

## Branch B: Finality / Participation

Use this branch when finalized checkpoints lag or participation drops.

1. Validate finality with direct Ethnode checkpoints across multiple nodes when possible.
2. Use Dora completed epoch summaries, not the head epoch, to estimate participation.
3. Identify whether missing votes are concentrated by validator range, node, client, region, or fork side.
4. Check whether a split explains apparent offline validators.
5. Check node sync and peer counts for implicated nodes.
6. Search for fork-specific participation changes if the issue starts near a fork boundary.

Thresholds:

- Finality requires more than 2/3 of stake attesting correctly.
- Normal finality lag is 2 epochs.
- More than 4 epochs without finality is concerning.
- More than 8 epochs suggests a significant network issue.

## Branch C: Missed Beacon Blocks

Use this branch for slots with no beacon block at all.

Pre-ePBS and ePBS both have missed beacon blocks. Under ePBS, do not confuse this with a canonical block that has a missing execution payload.

1. Collect missed slots from Dora or direct beacon API.
2. Identify scheduled proposer indices and map them to nodes/validator ranges using Dora, inventory, or validator-range API.
3. Query proposer node health: Ethnode sync, peer count, version, logs.
4. Inspect CL logs first, then validator-client logs if separated.
5. If the same node misses repeatedly, classify as node-local.
6. If one client type misses across nodes, classify as client-specific and compare image versions.

## Branch D: ePBS / Gloas Missing Payloads

Use this branch only when the active protocol model says ePBS/Gloas is active or block bodies contain `signed_execution_payload_bid`.

Under ePBS, slot state has at least three cases:

| State | Signal | Meaning | Investigate |
| --- | --- | --- | --- |
| Block + payload | canonical block and payload present | Healthy slot | No incident by itself |
| Missed beacon block | no beacon block / Dora `status=Missing` | Proposer produced no beacon block | Proposer node |
| Missing payload | canonical beacon block but payload absent | Builder won bid but payload was not revealed in time | Builder/buildoor path |

Dora `with_eth_block=false` is a useful first pass, but the authoritative verdict is PTC `payload_attestations` in the next block.

### Collect Missing Payload Candidates

Use `search(type="examples", query="Find ePBS missing payload candidates")`. Treat this as a fast Dora first pass only; it identifies slots to confirm with PTC verdicts.

### Confirm With PTC Verdicts

The PTC verdict for slot `S` is carried in `payload_attestations` in the block at slot `S+1`.

Use `search(type="examples", query="Get PTC payload facts for a slot")` for individual slots.

### Attribute Missing Payloads To Builders

Use `search(type="examples", query="Summarize payload presence by builder")`.

Interpretation rules:

- One `builder_index` concentrating absent payloads is a builder reveal problem until proven otherwise.
- `builder_index = 18446744073709551615` means self-build. Missing payload on self-build points to a proposer/local EL path, not an external builder.
- A small number of missing payloads does not by itself explain finality failure. Scope it to the responsible builder unless finality/participation evidence says otherwise.
- Healthy builders and self-build should have near-100% payload-present rates over a stable window.

### Map `builder_index` To A Buildoor Node

Builder indices are registry indices, not validator indices. Do not map `builder_index` through `/validators/{index}`. Map it through builder sidecar logs.

Use `search(type="examples", query="Map ePBS builder index to host")`.

Builder nodes commonly look like `buildoor-<cl>-<el>-<n>`, but always use inventory/log evidence instead of relying on the name.

### Investigate Builder Reveal

For absent-payload slots, pull builder logs around the slot.

Use `search(type="examples", query="Builder reveal logs for a slot window")`.

Expected proposer-side behavior for a missing payload can include accepting a bid, publishing a block, then PTC voting `payload_present=false`. That rules the proposer out; it does not prove proposer fault.

Builder-side failure patterns:

- `Failed to submit reveal ... status 400` - likely builder/CL serialization or version mismatch.
- `Giving up on reveal after max attempts` - builder reveal path failed.
- `Published data columns to 0 peers` - likely data availability or peering propagation issue.
- One CL implementation fails while another builder works - likely client-specific envelope endpoint compatibility.

Builder sidecar components to recognize:

| Component | Key lines | Use |
| --- | --- | --- |
| `bid-creator` | `Bid submitted ... builder_index=N` | maps builder index to host |
| `scheduler` | `Creating and submitting bid`, `Revealing payload`, `Failed to submit reveal`, `Giving up on reveal` | reveal state machine |
| `reveal-handler` | `Including blobs and kzg proofs`, `Payload revealed` | envelope assembly/publish |
| `builder-service` | `Payload attributes event received`, `Starting payload build`, `Our payload was included on-chain` | build trigger and win detection |
| `epbs-service` | `Head event received`, `Our payload was included in a beacon block` | chain-head tracking |
| `bid-tracker` | `Recorded won bid`, `Revealed bid` | bid accounting |

## Branch E: EL / Engine API / Spec Mismatch

Use this branch when CL logs show payload validation failures, engine API errors, execution sync failures, or when a divergent block touches execution-rule EIPs.

1. Identify the exact slot/block/transaction/payload that triggered the error.
2. Pull CL logs for engine API errors on the proposer and peers.
3. Pull EL logs on the same node and matching client cohort.
4. Query the execution block or transaction through Ethnode if available.
5. Search EIPs and execution specs for the active feature or changed invariant.
6. Compare affected and unaffected client pairs and image versions.

Diagnostic matrix:

| Evidence | Likely class |
| --- | --- |
| CL errors only | consensus client/fork-choice/validation issue |
| CL engine API errors plus EL errors | execution payload/state transition issue |
| CL clean but EL errors | EL local issue or non-primary cascading issue |
| All nodes of one EL fail | EL client bug or execution-spec mismatch |
| All nodes of one CL fail | CL client bug or consensus-spec mismatch |
| Error begins exactly at fork epoch | fork activation/config/spec compatibility issue |

## Branch F: DA / Blob / PeerDAS

Use this branch when the active fork includes blob sidecars or data columns and symptoms mention blobs, payload reveal, missing data, column unavailability, or propagation warnings.

1. Record the blob schedule and active blob/data-column limits from `networks://<network>` and beacon config.
2. Query block bodies/sidecars for affected slots.
3. On ePBS, inspect PTC `blob_data_available` when present.
4. Search consensus specs for the relevant Fulu/PeerDAS/data-column behavior.
5. Check CL logs for sidecar/data-column publish, subscribe, custody, and peer warnings.
6. If payload reveal fails with data propagation warnings, correlate builder logs with DA logs before blaming execution payload construction.

## Phase 4: Log and Node Drilldown

Hosted devnets and many hosted test networks run services as containers on VMs. Their logs are shipped via OpenTelemetry into `clickhouse-raw`, database `external`, table `external.otel_logs`. Query with `clickhouse.query("clickhouse-raw", ...)`.

Always filter by:

- `ResourceAttributes['network']`
- `Timestamp`

Key fields:

- `Timestamp DateTime64(9)` - partition key; always filter
- `Body String` - raw log line; severity is often embedded here
- `SeverityText LowCardinality(String)` - often empty for Docker logs
- `ResourceAttributes['host.name']` - node/host label
- `LogAttributes['log.file.name']` - container json-log file
- `LogAttributes['container_id']` - container id when available

There is no universal `ethereum_cl` or `ethereum_el` label in these logs. A host may run CL, EL, validator, builder, relay, and sidecars. Identify containers by inventory and sample log lines.

### Discover Host Containers

Use `search(type="examples", query="List containers on a hosted node")`.

Use samples to identify client formats:

- Lighthouse: timestamped `INFO/WARN/ERRO` style consensus logs
- Prysm: `level=... msg=...`
- Teku: Java/log4j style lines
- Geth: `LEVEL [MM-DD|HH:MM:SS.mmm] ...`
- Builder sidecar/buildoor: `level=... msg="..." component=...`

### Fetch Severe Logs

Use `search(type="examples", query="Recent node errors")`. After identifying the relevant container, use `search(type="examples", query="Logs for one container in a time window")`.

If severe logs are inconclusive, broaden to warnings, then to a tight INFO window around the affected slot. Unfiltered log queries are expensive and noisy.

### Sweep A Client Type

Use inventory when possible. If you must use host naming conventions, treat them as hints only:

- CL prefix: `host.name LIKE 'lighthouse-%'`
- EL inside pair: `host.name LIKE '%-geth-%'`
- Builder: `host.name LIKE 'buildoor-%'`

Use `search(type="examples", query="Errors across a consensus client type")` for a CL-client sweep.

Always append the exact host list used. Do not invent missing nodes from a naming pattern.

### Node Health Validation

If logs are missing for a host, validate with Ethnode when available:

- beacon sync status
- CL peer count
- execution sync status
- EL peer count
- node version

A node can be healthy but not shipping logs, or down but still present in inventory.

## Phase 5: Root Cause Analysis

Classify the issue by scope, layer, and protocol actor.

Scope:

- Single node failure - local crash, disk, OOM, sync, peers, misconfiguration.
- Client-specific failure - all or most nodes of one CL/EL/client version affected.
- Builder-specific failure - one ePBS builder has poor reveal rate or reveal errors.
- Network split - multiple incompatible chain views.
- Widespread cross-client failure - infrastructure, config, or spec/fork edge case.

Layer/actor:

- Consensus proposer
- Attester/validator client
- Builder/buildoor sidecar
- PTC/payload attestation path
- Execution client / engine API
- Data availability / sidecar / PeerDAS
- Infrastructure/logging/host

Hypotheses to test:

- Did the problem start at a fork boundary, deployment, restart, or config change?
- Does one client pair or image version explain the affected set?
- Does a specific block, payload, transaction, blob, or data column explain the first failure?
- Does a spec/EIP search identify changed semantics the old runbook would misclassify?
- Do direct node RPC and indexed Dora/Forky views agree?
- Do logs show cause before symptom, or only downstream effects?

Append rejected hypotheses too when they prevent misattribution.

## Phase 6: Final User Report

The final response MUST include:

- what is happening, in plain language
- the active protocol model used for interpretation
- the likely root cause and confidence level
- affected nodes, clients, validators, builders, slots, epochs, or blocks
- source disagreements and data-quality caveats
- next actions: restart, disable/deprioritize builder, collect more logs, file client bug, check infra, compare code/spec, etc.
- citations for every concrete artifact
- debug report URL and local `/workspace/...` path

Do not overstate certainty. If evidence only narrows the issue to a class, say that and name the next query that would distinguish the remaining possibilities.

## Reference: ePBS / Gloas Glossary

- ePBS - enshrined Proposer-Builder Separation, EIP-7732.
- Gloas - consensus-spec fork name associated with ePBS.
- Builder - entity that builds the execution payload and bids for inclusion.
- `builder_index` - on-chain registry index for a builder. It is not necessarily a validator index.
- Self-build - `builder_index = 18446744073709551615`; proposer built locally.
- Bid - `signed_execution_payload_bid` in the beacon block; commitment/value, not full payload.
- Reveal / envelope - builder publication of the signed execution payload envelope.
- PTC - Payload Timeliness Committee.
- `payload_attestations` - next-block attestations containing `payload_present` and, when available, `blob_data_available`.
- Missing payload - canonical beacon block whose committed execution payload was not revealed in time.

## Reference: Key Thresholds

| Signal | Healthy | Warning / incident |
| --- | --- | --- |
| Finality | finalized checkpoint advancing, normal lag around 2 epochs | more than 4 epochs concerning; more than 8 epochs significant |
| Participation | more than 66.7% effective stake attesting correctly | below 66.7% risks no finality |
| Fork count | one canonical fork | multiple forks require divergence investigation |
| ePBS payload reveal rate | near 100% per healthy builder/self-build | one builder materially below peers is a builder incident |
| Logs | isolated errors with no chain symptom may be noise | errors correlated with first bad slot/epoch are high value |
