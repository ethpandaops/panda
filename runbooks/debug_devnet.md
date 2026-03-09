---
name: Debug Devnet
description: Collect information about a devnet or systematically debug issues using Dora and Loki to diagnose network splits, offline nodes, finality delays, and client bugs
tags: [devnet, debugging, network-split, forks, logs, consensus, validators, status, info]
prerequisites: [loki]
---

The first step in debugging a devnet is discovering which datasources have the network, then gathering information from whatever is available. Not all devnets are registered in Dora — some only have Loki logs. Phase 0 determines the data profile so the debug flow adapts accordingly.

**The user MUST specify which network to debug.** Do NOT assume a network — if the user hasn't specified one, ask them before proceeding. You can show available networks with `dora.list_networks()` or check Loki labels via `loki.get_label_values("ethpandaops", "testnet")` to help them choose.

Refer to the query skill for general API usage patterns (Dora overview, Loki label discovery, direct HTTP calls, Dora link generation, etc.). This runbook only covers the debugging-specific procedure and API calls not in the skill.

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

**Appending to the debug report:** In every subsequent step, read the path from `/workspace/debug_file_path.txt` and append with `open(debug_file, "a")`. Do this for every piece of data collected — raw API responses, log extracts, summaries, and theories. Do not repeat this boilerplate in every step; apply the pattern consistently.

## Timeframe Rules

All steps in this runbook MUST use the same consistent timeframe OR there must be a reason to change the timeframe. Determine the **active timeframe** once and use it everywhere. If you update the **active timeframe** mid debugging, then mention it in the raw dump:

1. If the user provides a specific timeframe or epoch range → use that
2. If a network split is detected in step 1 → override to the divergence slot/epoch and investigate around that point (before and after)
3. Otherwise → default to the **past 1 hour**

Once set, use it consistently for: epoch queries, slot lookbacks (~300 slots per hour, 1 slot ≈ 12s), Loki log queries, and all correlation. Do NOT mix timeframes across step UNLESS needed.

## Phase 0: Network Discovery

Before collecting data, determine which datasources have the target network. This avoids wasted calls and adapts the debug flow to what is actually available.

0. **Determine the data profile** — In a single `execute_python` call, check all datasources for the target network:

   ```python
   # Check Dora
   try:
       networks = dora.list_networks()
       has_dora = "<network>" in [n["name"] for n in networks]
   except Exception:
       has_dora = False

   # Check Loki
   try:
       testnets = loki.get_label_values("ethpandaops", "testnet")
       has_loki = "<network>" in testnets
   except Exception:
       has_loki = False

   # If Loki is available, also discover instances for later use
   instances = []
   if has_loki:
       try:
           instances = loki.get_label_values("ethpandaops", "instance", f'{{testnet="{network}"}}')
       except Exception:
           pass

   # Check ethnode (direct node API access)
   import os
   has_ethnode = os.environ.get("ETHPANDAOPS_ETHNODE_AVAILABLE") == "true"
   ```

   Record the **data profile** in the debug report:
   - `has_dora: true/false`
   - `has_loki: true/false`
   - `has_ethnode: true/false`
   - List of discovered instances (if Loki is available)

   **Routing rules:**
   - If the network is not found in **any** datasource → report to the user that the network doesn't exist in any known datasource and **stop**.
   - `has_dora = true` → Phase 1 (Dora) runs normally.
   - `has_dora = false` → **Skip Phase 1 entirely.** Note in the debug report that Dora is unavailable. If `has_ethnode = true`, use ethnode to build a basic network baseline before proceeding to Phase 2 — query head slots, finality checkpoints, and sync status across discovered instances to approximate what Dora would have provided (see Phase 1 fallback below).
   - `has_loki = false` → Phase 2 is limited; note that log investigation is unavailable.
   - `has_ethnode = true` → Direct node RPC queries are available in Phase 3 for hypothesis validation.

## Phase 1: Data Collection with Dora

**Skip this phase if Phase 0 determined `has_dora = false`.** If `has_ethnode = true`, use the ethnode extension (`search(type="examples", query="ethnode")` for patterns) to build a partial baseline: query head slots/roots, finality checkpoints, and sync status across the discovered instances. This helps answer the baseline questions in step 2 (single fork vs split, finalizing, which nodes are behind) without Dora. Append results to the debug report, then proceed to Phase 2.

1. **Collect all Dora data** - In a single step, gather all network data and append raw responses to the debug report. You MAY combine these into one `execute_python` call:

   - **Network overview** — use `search(type="examples", query="network overview")` for the pattern. Note: `current_slot` is `epoch * 32` (epoch's first slot), not actual head slot.
   - **Network splits** — use `search(type="examples", query="network splits")`. A healthy network has one fork.
   - **Epoch details** — use `search(type="examples", query="epoch summary")`. Iterate through ~9 epochs per hour across the active timeframe. **Always start from head epoch - 1** (the most recent completed epoch) — the head epoch is still in progress and will show artificially low participation. You SHOULD use try/except per epoch to handle failures without crashing.
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

   Append the baseline summary to the debug report as a readable narrative. You SHOULD generate Dora links for relevant epochs, slots, and validators using the `dora.link_*()` helpers (see query skill).

   **If Dora shows a healthy network** (no splits, finality on track, high participation, no offline nodes) but the user reports issues, present the healthy baseline to the user and ask them for more details about what they're observing. You MAY proceed to Loki only if you have a specific target — otherwise let the user guide the next step.

## Phase 2: Log Investigation with Loki

**If Dora was available (Phase 1 ran):** Use the Dora findings to target specific offline or problematic nodes. You SHOULD always use label filters — unfiltered logs are slow and may time out.

**If Dora was unavailable (Loki-only mode):** Start with broad label discovery to understand the network topology, then fetch CRIT/ERR logs across all CL clients to identify which nodes have issues. Since there is no Dora baseline, you need to build a basic picture from logs alone — which clients are present, are they producing logs, are there widespread errors or isolated ones.

The standard Loki instance is `"ethpandaops"`. Refer to the query skill for Loki label discovery and query patterns.

**Use the same active timeframe** established in the Timeframe Rules section above.

**Instance naming convention:** Node instance names follow the format `<cl_type>-<el_type>-<instance_number>` (e.g. `lighthouse-geth-1`, `prysm-nethermind-2`, `teku-besu-1`). Use this to target the right Loki labels:
- `ethereum_cl` = the first part (e.g. `lighthouse`)
- `ethereum_el` = the second part (e.g. `geth`)
- The full name maps to the `instance` label

**You SHOULD start with the consensus layer (CL).** The network moves forward via the CL — block proposals, attestations, and finality are all CL concerns. Most devnet issues originate at the CL level. Only investigate EL logs after reviewing CL logs, and only if the CL logs suggest the problem is on the execution side (e.g. payload validation errors, engine API failures, execution timeouts).

3. **Discover Loki labels** - In Loki-only mode (no Dora), you MUST fetch available labels and values from the `"ethpandaops"` Loki instance to understand the network topology — this is the only way to discover what nodes exist. When Dora is available, you MAY fetch labels to confirm that the expected `testnet`, `ethereum_cl`, `ethereum_el`, and `instance` labels exist and contain the target network/nodes. Append to debug report.

4. **Fetch CL logs first (CRIT/ERR)** - For each problematic node (or all CL clients in Loki-only mode), query CL logs at the most severe log levels:

   ```
   {testnet="<network>", ethereum_cl="<cl_type>", instance="<instance_name>"} |~ "(?i)(CRIT|ERR)"
   ```

   Log level formats vary by client — see the query skill's Loki section for format details and fallback strategies.

   If multiple nodes are offline, you MUST query each one. Look for common error patterns across nodes — the same error on multiple CL nodes likely points to a shared cause (CL client bug, consensus rule issue).

   **If Loki returns no logs at all** for a node, that is itself a signal — but it does not necessarily mean the node is down (it may just not be shipping logs). If `has_ethnode = true`, verify by querying the node directly (e.g. sync status or health check). If the node responds, it is running but not logging; if it is unreachable, it is truly down. Report either finding to the user.

5. **Fetch EL logs if CL points to execution issues** - You MAY investigate EL logs if CL logs show:
   - Engine API errors (e.g. `engine_newPayload` failures, timeouts)
   - Payload validation failures
   - Execution sync issues
   - "execution client unavailable" or similar

   If any appear, fetch EL logs for the same node using `ethereum_el="<el_type>"` with the same filter pattern.

   **CL/EL diagnostic matrix** — use this to narrow the root cause:
   - **Errors only in CL logs** → consensus issue (attestation bug, fork choice problem, CL client bug)
   - **CL engine API errors + EL errors** → execution issue (invalid block, state transition bug, EL client bug)
   - **CL clean but EL errors** → EL struggling but CL compensating; monitor but may not be primary cause
   - **Both layers erroring** → shared dependency (disk, memory, network) or cascading failure

6. **Escalate to WARN/INFO if needed** - If CRIT/ERR logs are empty or inconclusive at both CL and EL levels, broaden to WARN, then INFO. You MAY go to DEBUG as a last resort — DEBUG logs are very verbose and may time out.

7. **Correlate logs with Dora timeline** - **Only applicable when Dora data exists (Phase 1 ran).** You SHOULD match log timestamps against the Dora data:
   - When did errors start relative to missed slots or participation drops?
   - Do errors correlate with a specific epoch or slot?
   - Are errors from one client type or spread across multiple?
   - Are the errors at the CL level, EL level, or both?
   - If the network has split, compare logs from nodes on different forks to find the divergence point

This concludes the **data collection phase**. If Dora was available, you should now have: a Dora baseline (network state, split status, offline proposers/attesters), targeted CL logs (and EL logs if relevant) from the problematic nodes, and an understanding of which layer the errors originate from. If Loki-only, you should have: a log-driven baseline (network topology from labels, error patterns across clients, which nodes are healthy vs problematic), and an understanding of the error landscape without Dora context.

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
   - If you suspect a specific EIP is involved, and an EIP skill is available, use it to fetch the exact specification to confirm or rule out a faulty implementation.

   Append theories and reasoning to the debug report.

### RPC Validation (requires `has_ethnode = true`)

**If the ethnode extension is available**, use direct node RPC queries via `from ethpandaops import ethnode` to validate hypotheses and gather concrete proof. Use `search(type="examples", query="ethnode")` for API patterns. Target the instances discovered in Phase 0 or identified as problematic in Phases 1–2.

**When to use RPC:**
- **Network split suspected** → compare head slots/roots and finality checkpoints across nodes on different forks; fetch the divergence block from each side
- **Node offline/stuck** → check sync status and peer counts to confirm whether the node is down, syncing, or isolated
- **Verifying a hypothesis** → when you need to confirm or rule out a theory, query the relevant nodes directly using curated functions or generic pass-through (`beacon_get`, `execution_rpc`) to get concrete evidence that strengthens the root cause analysis
- **Finality stalled** → compare finality checkpoints across all nodes to find disagreements

Append all RPC query results and analysis to the debug report.

9. **Summarize findings** - You MUST present the user with:
   - A clear description of what is happening (symptoms)
   - The most likely root cause and supporting evidence
   - Which nodes/clients are affected
   - Dora links for relevant slots, epochs, and validators (if Dora was available)
   - Suggested next steps (e.g. restart a node, report a client bug, check infrastructure)

   Append the summary to the debug report. You MUST provide the user with the file path.

## Key Thresholds

- Finality requires >66.7% (2/3) of stake attesting correctly
- Normal finality lag is 2 epochs (~13 minutes on mainnet, varies on devnets)
- >4 epochs without finality is cause for concern
- >8 epochs suggests a significant network issue
