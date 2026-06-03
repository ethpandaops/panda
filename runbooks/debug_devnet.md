---
name: Debug Devnet
description: Collect information about a devnet or systematically debug issues using Dora and OTel ClickHouse logs to diagnose network splits, offline nodes, finality delays, and client bugs
tags: [devnet, debugging, network-split, forks, logs, consensus, validators, status, info]
prerequisites: [clickhouse-raw]
---

The first step in debugging a devnet is discovering which datasources have the network, then gathering information from whatever is available. Not all devnets are registered in Dora — some only have container logs in ClickHouse. Phase 0 determines the data profile so the debug flow adapts accordingly.

**This runbook is for proper, multi-VM devnets and testnets** (e.g. `fusaka-devnet-0`, `hoodi`) — networks deployed across bare-metal VMs. The `investigate` skill routes here when the target is found in the hosted datasources; local Kurtosis devnets are routed to the **Debug Local Devnet** runbook instead. You are therefore always working with a known, named network.

**The user MUST specify which network to debug.** Do NOT assume a network — if the user hasn't specified one, ask them before proceeding. You can list the proper devnets and testnets that exist with `dora.list_networks()`.

Refer to the query skill for general API usage patterns (Dora overview, ClickHouse queries, direct HTTP calls, Dora link generation, etc.). This runbook only covers the debugging-specific procedure and API calls not in the skill.

## How Devnet Logs Flow

Hosted devnets run as Docker containers on bare-metal VMs (managed by Ansible). Each container's logs are scraped and shipped via OpenTelemetry into the `clickhouse-raw` ClickHouse cluster, database `external`, table `external.otel_logs`. Query them with SQL via `clickhouse.query("clickhouse-raw", ...)`, always filtering by `ResourceAttributes['network']` (the devnet) and `Timestamp`. **There is no hosted Loki — devnet container logs live only in ClickHouse.**

Key fields on `external.otel_logs`:
- `Timestamp DateTime64(9)` — always filter on this (it is the partition key).
- `Body String` — the raw log line. The level is usually embedded here, not in `SeverityText`.
- `SeverityText LowCardinality(String)` — often EMPTY for raw Docker logs; do not rely on it. Use `match(Body, ...)` for severity triage.
- `ServiceName` — empty for these VM/Docker logs (the `k8s.*` materialized columns are also empty — those only apply to Kubernetes platform logs).
- `ResourceAttributes Map(String, String)` — node identity. Keys: `network` (devnet name), `host.name` (the node, e.g. `lighthouse-geth-super-1`), `ingress_user`, `deployment.environment`.
- `LogAttributes Map(String, String)` — per-line attributes. Keys include `log.file.name` / `log.file.path` (the Docker container json-log file — one per container on the node), `container_id`, plus any structured fields the client emits (`level`, `msg`, `component`, ...).

**Node naming:** `host.name` encodes the client pair as `<cl>-<el>-<tier>-<index>` (e.g. `lighthouse-geth-super-1` → CL lighthouse, EL geth). Non-paired nodes exist too (`bootnode-1`, `mev-relay-1`). **There is no `ethereum_cl` / `ethereum_el` label like Loki had** — a node VM runs the CL, EL, validator, and sidecar containers together, distinguished only by `LogAttributes['log.file.name']` (a container hash). To isolate one client's logs on a node, discover its containers first (see Phase 2) or identify the client by its log-line format in `Body`.

## Debug Report

At the start of each debug session, create a single file at `/workspace/<network>-debug-<timestamp>.md`. Append ALL raw API responses, log extracts, and analysis notes to this file as you go. At the end of the session, provide the user with the file path.

Initialize the file and a helper for appending:
```python
from datetime import datetime
import json

network = "<network>"
timestamp = datetime.utcnow().strftime("%Y%m%d-%H%M%S")
debug_file = f"/workspace/{network}-debug-{timestamp}.md"

with open(debug_file, "w") as f:
    f.write(f"# Debug Report: {network}\n")
    f.write(f"**Generated:** {datetime.utcnow().isoformat()}Z\n\n")

# Save the path for subsequent steps
with open("/workspace/debug_file_path.txt", "w") as f:
    f.write(debug_file)
```

**Appending to the debug report:** In every subsequent step, read the path from `/workspace/debug_file_path.txt` and append with `open(debug_file, "a")`. Do this for every piece of data collected — raw API responses, log extracts, summaries, and theories.

## Verbatim Tool Output

When reporting label values, instance names, counts, or log lines: paste the raw tool response in a fenced code block. Do NOT paraphrase, reformat, infer, or "reconstruct" output. If the tool returns structured data that cannot be pasted as-is, say so explicitly — never invent entries to fill the gap.

If the user states a fact (e.g. "we have 16 nodes"), do not let it bias tool output. Report what the tool returned, even if it contradicts the user.

If two sources disagree (e.g. Dora says 16 nodes, the logs show 30 hosts), surface the disagreement rather than picking one. Dora is authoritative for *what nodes exist*; the OTel logs are authoritative for *what nodes are shipping logs*. They are not interchangeable.

## Citations

A *citation* is a `panda` command that re-derives the cited evidence. Every finding you record — both in the debug report and in chat output — MUST be followed by the citation(s) that produce it, so the user can run them and verify independently. Citations are claim-anchored, not exhaustive: cite the calls that support a finding, not every probe along the way.

Place each citation directly under the finding, in a fenced shell block, with a one-line `#` comment saying what it fetches. Discover the current command surface with `panda --help` (and subcommand `--help`) — do not hardcode flags or subcommands from memory.

## Timeframe Rules

All steps in this runbook MUST use the same consistent timeframe OR there must be a reason to change the timeframe. Determine the **active timeframe** once and use it everywhere. If you update the **active timeframe** mid debugging, then mention it in the raw dump:

1. If the user provides a specific timeframe or epoch range → use that
2. If a network split is detected in step 1 → override to the divergence slot/epoch and investigate around that point (before and after)
3. Otherwise → default to the **past 1 hour**

## Search-First Principle

Use the `search` tool to find relevant patterns, related procedures, and protocol context throughout this runbook:

- **Examples** — Phase 1 already references specific example searches. In other phases, if you need a query pattern you don't have (e.g., Prometheus metrics for node health, EL-specific ClickHouse queries), search for it rather than guessing: `search(type="examples", query="<what you need>")`
- **Runbooks** — If you hit a sub-problem during debugging that feels like it deserves its own procedure (e.g., "finality stalled but no split", "single client type failing"), check whether a dedicated runbook exists: `search(type="runbooks", query="<sub-problem>")`
- **EIPs** — When investigating protocol-level behavior, fork boundary issues, or suspected consensus rule edge cases, look up the relevant specification: `search(type="eips", query="<EIP topic or number>")`

This does NOT replace the hardcoded patterns in this runbook — those encode debug-specific knowledge that generic examples won't have. Use search to fill gaps.

## Phase 0: Network Discovery

Before collecting data, determine which datasources have the target network.

0. **Discover datasources and determine the data profile** — Do not assume node names. First discover what is available, then check for the target network:

   ```python
   from ethpandaops import dora, clickhouse
   import os

   network = "<network>"

   # Check Dora
   try:
       networks = dora.list_networks()
       has_dora = network in [n["name"] for n in networks]
   except Exception:
       has_dora = False

   # Check ClickHouse OTel logs — is this network shipping container logs to external.otel_logs?
   # The same query also discovers the node (host.name) list for later use.
   has_logs = False
   hosts = []
   try:
       df = clickhouse.query("clickhouse-raw", """
           SELECT DISTINCT ResourceAttributes['host.name'] AS host
           FROM external.otel_logs
           WHERE ResourceAttributes['network'] = {network:String}
             AND Timestamp >= now() - INTERVAL 1 HOUR
           ORDER BY host
       """, {"network": network})
       hosts = [h for h in df["host"].tolist() if h]
       has_logs = len(hosts) > 0
   except Exception:
       pass

   # Check ethnode (direct node API access)
   has_ethnode = os.environ.get("ETHPANDAOPS_ETHNODE_AVAILABLE") == "true"

   print(f"has_dora={has_dora}, has_logs={has_logs}, has_ethnode={has_ethnode}")
   print(f"hosts={hosts}")
   ```

   Record the **data profile** in the debug report:
   - `has_dora: true/false`
   - `has_logs: true/false`
   - `has_ethnode: true/false`
   - List of discovered nodes (`host.name` values, if logs are available)

   **Routing rules:**
   - If the network is not found in **any** datasource → report to the user that the network doesn't exist in any known datasource and **stop**.
   - `has_dora = true` → Phase 1 (Dora) runs normally.
   - `has_dora = false` → **Skip Phase 1 entirely.** Note in the debug report that Dora is unavailable. If `has_ethnode = true`, use ethnode to build a basic network baseline before proceeding to Phase 2 — query head slots, finality checkpoints, and sync status across discovered nodes to approximate what Dora would have provided (see Phase 1 fallback below).
   - `has_logs = false` → Phase 2 is limited; note that log investigation is unavailable.
   - `has_ethnode = true` → Direct node RPC queries are available in Phase 3 for hypothesis validation.

## Phase 1: Data Collection with Dora

**Skip this phase if Phase 0 determined `has_dora = false`.** If `has_ethnode = true`, use the ethnode module (`search(type="examples", query="ethnode")` for patterns) to build a partial baseline instead. Then proceed to Phase 2.

1. **Collect all Dora data** - In a single step, gather all network data and append raw responses to the debug report. You MAY combine these into one `execute_python` call:

   - **Network overview** — use `search(type="examples", query="network overview")` for the pattern. Note: `current_slot` is `epoch * 32` (epoch's first slot), not actual head slot.
   - **Network forks** — use `search(type="examples", query="network splits")`. Query the Dora `/forks` endpoint (with `Accept: application/json` header) to detect splits. A healthy network has one fork.
   - **Epoch details** — use `search(type="examples", query="epoch summary")`. Iterate through ~9 epochs per hour across the active timeframe. **Always start from head epoch - 1** (the most recent completed epoch) — the head epoch is still in progress and will show artificially low participation. You SHOULD also check the head epoch, but treat its data as preliminary. Use try/except per epoch.
   - **Missing proposers** — use `search(type="examples", query="missing proposers")`. Adjust `slot_lookback` to match the active timeframe (~300 slots per hour).
   - **Offline attesters** — use `search(type="examples", query="offline attesters")`.

   If there are multiple forks:
   - **IMPORTANT:** A network split overrides the active timeframe. You MUST identify the divergence slot/epoch where the split occurred and refocus the entire investigation around that point. All subsequent steps MUST use this divergence-centered timeframe.
   - Participation and proposer data from Dora reflects the canonical fork — nodes on a minority fork will appear "offline" even though they may be running fine on their fork
   - The root cause investigation should focus on **why the split happened** rather than individual node failures
   - When checking logs later, compare logs from nodes on different forks to find the divergence point

2. **Build a baseline summary** - You MUST summarize the network state before proceeding to log investigation:
   - Is the network on a single fork or has it split? (if split, this likely explains most other symptoms)
   - Is the network finalizing? How many epochs behind?
   - What is the participation rate? (>66.7% required for finality) **Use the last completed epoch, not the head epoch** — the head epoch is still in progress and will report misleadingly low participation.
   - Are there missed slots or empty epochs?
   - Which specific nodes/validators are offline or underperforming?
   - If there are multiple forks, which nodes are on which fork?

   Append the baseline summary to the debug report as a readable narrative. You SHOULD generate Dora links for relevant epochs, slots, and validators using the `dora.link_*()` helpers (see query skill). **Cite each named node, validator, slot, or fork-tip block per the Citations section.**

   **If Dora shows a healthy network** (no splits, finality on track, high participation, no offline nodes) but the user reports issues, present the healthy baseline to the user and ask them for more details about what they're observing. You MAY proceed to log investigation only if you have a specific target — otherwise let the user guide the next step.

## Phase 2: Log Investigation with ClickHouse (`external.otel_logs`)

Use Dora findings (if available) to target specific nodes. With logs only (no Dora), start from the `hosts` list discovered in Phase 0 to identify which nodes have issues. **Always filter by `ResourceAttributes['network']` and `Timestamp`** — unfiltered queries scan everything and may time out. All queries go through `clickhouse.query("clickhouse-raw", ...)` against `external.otel_logs`; see the query skill's log section for the full schema and severity-matching details.

**Use the same active timeframe** established in the Timeframe Rules section above.

**Node naming:** Most nodes follow `<cl>-<el>-<tier>-<n>` (e.g. `lighthouse-geth-super-1` → CL lighthouse, EL geth), but devnets also include bootnodes, MEV relays, and other non-paired nodes (`bootnode-1`, `mev-relay-1`) that do NOT match this pattern. Never derive node names from the convention — always use the `hosts` list discovered in Phase 0 (or Dora's `/v1/clients/consensus`).

**CL vs EL — important difference from Loki:** there is no `ethereum_cl` / `ethereum_el` label. A node VM runs the CL, EL, validator, and sidecar containers together; their logs are separated only by `LogAttributes['log.file.name']` (a per-container json-log file, named by hash). To investigate one client on a node, first discover its containers (step 3) and identify the CL/EL container by its log-line format, then filter on that log file. To sweep a client type across the network, filter on `host.name` (e.g. `host.name LIKE 'lighthouse-%'` for lighthouse-CL nodes, or `host.name LIKE '%-geth-%'` for geth-EL nodes) — but remember the result still mixes that node's CL/EL/sidecar lines.

**You SHOULD start with the consensus layer (CL).** Most devnet issues originate at the CL level. Only investigate EL logs if CL logs point to execution-side problems (e.g. payload validation errors, engine API failures).

3. **Discover nodes and their containers** - You already have the node (`host.name`) list from Phase 0. For a node you want to drill into, list its containers and a sample line from each so you can tell which is the CL, EL, validator, or sidecar:

   ```python
   from ethpandaops import clickhouse

   network = "<network>"
   host = "<host.name>"

   df = clickhouse.query("clickhouse-raw", """
       SELECT
         LogAttributes['log.file.name'] AS container_log,
         count() AS lines,
         any(substring(Body, 1, 120)) AS sample
       FROM external.otel_logs
       WHERE ResourceAttributes['network'] = {network:String}
         AND ResourceAttributes['host.name'] = {host:String}
         AND Timestamp >= now() - INTERVAL 1 HOUR
       GROUP BY container_log
       ORDER BY lines DESC
   """, parameters={"network": network, "host": host})
   print(df)
   ```

   Identify the client from each `sample` log format (e.g. lighthouse `MMM DD HH:MM:SS.mmm LEVEL ...`, geth `LEVEL [MM-DD|HH:MM:SS.mmm] ...`, prysm `level=... msg=...`). Append the node→container map to the debug report.

4. **Fetch CL errors first (CRIT/ERR)** - For each problematic node (or all CL nodes when there is no Dora target), fetch the most severe lines. `SeverityText` is usually empty for these Docker logs, so match severity on the raw `Body`:

   ```python
   from ethpandaops import clickhouse

   network = "<network>"
   host = "<host.name>"

   df = clickhouse.query("clickhouse-raw", """
       SELECT
         Timestamp,
         ResourceAttributes['host.name'] AS host,
         LogAttributes['log.file.name'] AS container_log,
         Body
       FROM external.otel_logs
       WHERE ResourceAttributes['network'] = {network:String}
         AND ResourceAttributes['host.name'] = {host:String}
         AND match(Body, '(?i)(crit|err|error|fatal)')
         AND Timestamp >= now() - INTERVAL 1 HOUR
       ORDER BY Timestamp DESC
       LIMIT 200
   """, parameters={"network": network, "host": host})
   print(df)
   ```

   Once you have identified the CL container's log file (step 3), add `AND LogAttributes['log.file.name'] = {container:String}` to isolate the CL client's lines from the EL and sidecars on the same node.

   To sweep a CL client type across the whole network instead of one node, replace the host filter with `AND ResourceAttributes['host.name'] LIKE {cl_prefix:String}` and pass e.g. `{"cl_prefix": "lighthouse-%"}`.

   If multiple nodes are erroring, query each one. Look for common error patterns across nodes — the same error across nodes of one client type points to a client bug.

   **If a node returns no logs at all**, that is itself a signal — but it does not necessarily mean the node is down (it may just not be shipping logs). If `has_ethnode = true`, verify by querying the node directly (e.g. sync status or health check). If it responds, it is running but not logging; if it is unreachable, it is truly down. Report either finding to the user.

5. **Fetch EL logs if CL points to execution issues** - You MAY investigate EL logs if CL logs show:
   - Engine API errors (e.g. `engine_newPayload` failures, timeouts)
   - Payload validation failures
   - Execution sync issues
   - "execution client unavailable" or similar

   If any appear, fetch the EL container's logs on the same node — use the same query as step 4, filtering on the EL container's `LogAttributes['log.file.name']` (identified in step 3).

   **CL/EL diagnostic matrix** — use this to narrow the root cause:
   - **Errors only in CL logs** → consensus issue (attestation bug, fork choice problem, CL client bug)
   - **CL engine API errors + EL errors** → execution issue (invalid block, state transition bug, EL client bug)
   - **CL clean but EL errors** → EL struggling but CL compensating; monitor but may not be primary cause
   - **Both layers erroring** → shared dependency (disk, memory, network) or cascading failure

6. **Escalate to WARN/INFO if needed** - If CRIT/ERR lines are empty or inconclusive at both CL and EL, broaden the `Body` pattern to include `warn`, then drop the severity filter entirely for INFO/DEBUG. Unfiltered-severity queries are verbose — keep a tight `Timestamp` window and a `LIMIT`, and they may still time out.

7. **Correlate logs with Dora timeline** - **Only applicable when Dora data exists (Phase 1 ran).** You SHOULD match log timestamps against the Dora data:
   - When did errors start relative to missed slots or participation drops?
   - Do errors correlate with a specific epoch or slot?
   - Are errors from one client type or spread across multiple?
   - Are the errors at the CL level, EL level, or both?
   - If the network has split, compare logs from nodes on different forks to find the divergence point

## Phase 3: Root Cause Analysis

8. **Identify root cause** - You SHOULD classify the issue by scope and layer, then formulate and test hypotheses.

   **By scope:**
   - **Single node failure** — one node is down, others healthy. Likely local (crash, disk full, OOM, misconfiguration).
   - **Client-specific failure** — all nodes of one client affected. Likely a client bug.
   - **Network split** — multiple forks detected. Focus on the divergence point.
   - **Widespread failure** — many nodes across clients. Likely infrastructure or consensus rule edge case.

   **By layer:** Use the CL/EL diagnostic matrix from step 5 to classify. Combine scope and layer to narrow the root cause (e.g. "client-specific CL failure" → CL client bug, "widespread EL failure" → execution rule edge case).

   **Hypotheses to test:**
   - Does the error message point to a known issue?
   - Did the problem start at a specific slot/epoch correlating with a config change, fork boundary, or deployment?
   - Are affected nodes in the same region or infrastructure?
   - If a network split occurred, what is the first block where forks diverge? What is special about that block?
   - If you suspect a specific EIP is involved, use `search(type="eips", query="<EIP topic or number>")` to fetch the specification and confirm or rule out a faulty implementation.

   Append theories and reasoning to the debug report. **When a hypothesis pinpoints a specific block, transaction, slot, validator, or instance, cite it per the Citations section so the user can verify it independently.**

### RPC Validation (requires `has_ethnode = true`)

**If the ethnode module is available**, use direct node RPC queries via `from ethpandaops import ethnode` to validate hypotheses and gather concrete proof. Use `search(type="examples", query="ethnode")` for API patterns. Target the instances discovered in Phase 0 or identified as problematic in Phases 1–2.

**When to use RPC:**
- **Network split suspected** → compare head slots/roots and finality checkpoints across nodes
- **Node offline/stuck** → check sync status and peer counts
- **Verifying a hypothesis** → query nodes directly via `beacon_get` / `execution_rpc`
- **Finality stalled** → compare finality checkpoints across all nodes

Append all RPC query results and analysis to the debug report. **The `panda ethnode` invocation that produced each RPC result is itself the citation for any finding it supports — record it alongside the result per the Citations section.**

9. **Summarize findings** - You MUST present the user with:
   - A clear description of what is happening (symptoms)
   - The most likely root cause and supporting evidence
   - Which nodes/clients are affected
   - Dora links for relevant slots, epochs, and validators (if Dora was available)
   - Suggested next steps (e.g. restart a node, report a client bug, check infrastructure)
   - **Citations** for every concrete artifact named above (block, transaction, slot, validator, instance) per the Citations section, so the user can independently verify each claim

   Append the summary to the debug report. You MUST provide the user with the file path.

## Key Thresholds

- Finality requires >66.7% (2/3) of stake attesting correctly
- Normal finality lag is 2 epochs (~13 minutes on mainnet, varies on devnets)
- >4 epochs without finality is cause for concern
- >8 epochs suggests a significant network issue
