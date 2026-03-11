---
name: Debug Local Devnet
description: Collect information about a local Kurtosis devnet or systematically debug issues using Dora, Loki, and direct node API access to diagnose network splits, offline nodes, finality delays, and client bugs
tags: [devnet, debugging, network-split, forks, logs, consensus, validators, status, info, kurtosis, local, local devnet, enclave]
prerequisites: []
---

The first step in debugging a local devnet is discovering what tooling is available in the Kurtosis enclave, then gathering information from whatever is present. Not all local devnets have Dora or Loki — it depends on how the user configured their Kurtosis run. Phase 0 determines the data profile so the debug flow adapts accordingly.

**The user MUST specify which enclave to debug.** Do NOT assume an enclave — if the user hasn't specified one, ask them before proceeding. You can discover running enclaves with `kurtosis enclave ls`.

**Local devnets do NOT have ClickHouse or xatu.** Do not attempt to use `clickhouse.query()` or any xatu-related datasources — these only exist on remote deployments.

Refer to the query skill for general API usage patterns (Dora overview, Loki label discovery, direct HTTP calls, Dora link generation, etc.). This runbook only covers the debugging-specific procedure and API calls not in the skill.

## Debug Report

At the start of each debug session, create a single file at `/workspace/<network>-debug-<timestamp>.md`. Append ALL raw API responses, log extracts, and analysis notes to this file as you go. At the end of the session, provide the user with the file path.

Initialize the file and a helper for appending:
```python
from datetime import datetime
import json

network = "<enclave-name>"
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

Before collecting data, determine what tooling is available in the Kurtosis enclave.

0. **Discover the enclave and its services** — Run `kurtosis enclave ls` to find running enclaves, then `kurtosis enclave inspect <enclave>` to list all services, their ports, and status.

   From the enclave inspect output, identify:

   **Node services:**
   - **CL services**: names matching `cl-*` (e.g., `cl-1-teku-geth`), their `http` port mappings
   - **EL services**: names matching `el-*` (e.g., `el-1-geth-teku`), their `rpc` port mappings
   - **VC services**: names matching `vc-*` (e.g., `vc-2-geth-prysm`)

   **Naming convention:** CL services follow `cl-{index}-{cl_type}-{el_type}`, EL services follow `el-{index}-{el_type}-{cl_type}`. E.g., `cl-1-teku-geth` → CL: teku, EL: geth.

   **Available tooling** — check whether these services exist:
   - **Dora** (block explorer): look for services containing `dora` in the enclave inspect output. If present, `has_dora = true` — note its `http` port.
   - **Loki** (logs): Loki does NOT appear in `kurtosis enclave inspect`. Instead, check if Loki is running by calling `curl -s http://localhost:3100/ready` — Kurtosis always runs Loki on port 3100 when enabled. If it responds, `has_loki = true`.
   - **Prometheus** (metrics): look for services containing `prometheus` in the enclave inspect output. If present, note its port.
   - Any other observability or debugging services the user may have included.

   **Service status:** confirm all services are RUNNING. Services not in RUNNING state are already a finding — note them in the debug report.

   Record the **data profile** in the debug report:
   - `has_dora: true/false`
   - `has_loki: true/false`
   - Enclave name
   - List of CL/EL/VC services with their localhost ports
   - List of tooling services with their ports

   **Routing rules:**
   - `has_dora = true` → Phase 1 (Dora) runs normally.
   - `has_dora = false` → Use direct CL/EL API queries to build a baseline instead (see Phase 1 fallback below).
   - `has_loki = true` → Phase 2 uses local Loki for log investigation.
   - `has_loki = false` → Phase 2 falls back to `kurtosis service logs`.

## Phase 1: Data Collection with Dora

**Skip this phase if Phase 0 determined `has_dora = false`.** Instead, build a baseline by querying the CL and EL nodes directly via their localhost ports from enclave inspect. For each CL node, fetch `/eth/v1/node/syncing`, `/eth/v1/beacon/headers/head`, and `/eth/v1/beacon/states/head/finality_checkpoints`. For each EL node, call `eth_blockNumber` and `eth_syncing` via JSON-RPC. Compare head slots/roots across nodes to detect splits, and check finality checkpoints. Append results to the debug report, then proceed to Phase 2.

1. **Collect all Dora data** - If Dora is available in the enclave, query it via its localhost port. In a single step, gather all network data and append raw responses to the debug report. You MAY combine these into one `execute_python` call:

   - **Network overview** — use `search_examples("network overview")` for the pattern. Note: `current_slot` is `epoch * 32` (epoch's first slot), not actual head slot.
   - **Network splits** — use `search_examples("network splits")`. A healthy network has one fork.
   - **Epoch details** — use `search_examples("epoch summary")`. Iterate through ~9 epochs per hour across the active timeframe. **Always start from head epoch - 1** (the most recent completed epoch) — the head epoch is still in progress and will show artificially low participation. You SHOULD also check the head epoch, but treat its data as preliminary since the epoch may not be finished — it is still useful for identifying offline proposers in recent slots. You SHOULD use try/except per epoch to handle failures without crashing.
   - **Missing proposers** — use `search_examples("missing proposers")`. Adjust `slot_lookback` to match the active timeframe (~300 slots per hour).
   - **Offline attesters** — use `search_examples("offline attesters")`.

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

   Append the baseline summary to the debug report as a readable narrative. If Dora is available, you SHOULD generate Dora links for relevant epochs, slots, and validators using the `dora.link_*()` helpers (see query skill).

   **If the baseline shows a healthy network** (no splits, finality on track, high participation, no offline nodes) but the user reports issues, present the healthy baseline to the user and ask them for more details about what they're observing. You MAY proceed to log investigation only if you have a specific target — otherwise let the user guide the next step.

## Phase 2: Log Investigation

### If Loki is available (`has_loki = true`)

Local Kurtosis Loki has a different label schema than remote ethpandaops Loki.

**Stream labels** (for `{}` selectors): `job`, `service_name`, `detected_level` — all services share `job="kurtosis"` and `service_name="kurtosis"`, so these cannot distinguish between nodes.

**JSON fields** (accessible via `| json`): The log body is JSON containing fields like `kurtosis_service_name`, `kurtosis_enclave_uuid`, `container_name`, `source`, and `log` (the actual log content). Use `| json` to parse these and filter by service.

**Trimming log payloads with `line_format`:** Loki returns full stream metadata (labels, container info, timestamps) for every log entry. This is verbose and wastes context window space. For local Kurtosis Loki, the actual log content is in the `log` JSON field. **Always** append `| line_format` after `| json` to extract only the log message:
```
| json | line_format "{{.log}}"
```
This dramatically reduces response size. Apply this to ALL Loki queries below.

**Example LogQL queries for local Kurtosis Loki:**

For all CL errors (using JSON parsing):
```
{job="kurtosis"} | json | kurtosis_service_name=~"cl-.*" | log=~"(?i)(CRIT|ERR|error)" | line_format "{{.log}}"
```

For a specific node:
```
{job="kurtosis"} | json | kurtosis_service_name="cl-2-prysm-geth" | log=~"(?i)(CRIT|ERR|error)" | line_format "{{.log}}"
```

For all EL errors:
```
{job="kurtosis"} | json | kurtosis_service_name=~"el-.*" | log=~"(?i)(ERR|error|WARN)" | line_format "{{.log}}"
```

You can also use simpler raw line matches if JSON parsing is slow (no `line_format` needed since there's no JSON parsing):
```
{job="kurtosis"} |~ "cl-2-prysm-geth" |~ "(?i)(CRIT|ERR|error)"
```

**Use the same active timeframe** established in the Timeframe Rules section above.

### If Loki is not available — fallback to kurtosis service logs

Use `kurtosis service logs` only when no Loki instance exists in the enclave:
- `kurtosis service logs <enclave> <service>` — logs for a specific service (default: last 200 lines)
- `kurtosis service logs <enclave> -x` — logs for **all** services
- `--regex-match "<pattern>"` — filter lines matching a regex (re2 syntax)
- `--match "<string>"` — filter lines containing a literal string
- `-n <count>` — number of lines to retrieve
- `-v` — invert the filter (lines NOT matching)
- `-a` — get all logs

### Log investigation procedure

Regardless of which log source is used, follow this procedure:

**You SHOULD start with the consensus layer (CL).** The network moves forward via the CL — block proposals, attestations, and finality are all CL concerns. Most devnet issues originate at the CL level. Only investigate EL logs after reviewing CL logs, and only if the CL logs suggest the problem is on the execution side (e.g. payload validation errors, engine API failures, execution timeouts).

3. **Discover Loki labels** - If using Loki, fetch available labels and values to understand the topology. Append to debug report.

4. **Fetch CL logs first (CRIT/ERR)** - For each problematic node (or all CL clients if no specific targets), query CL logs at the most severe log levels.

   Log level formats vary by client — see the query skill's Loki section for format details and fallback strategies.

   If multiple nodes are offline, you MUST query each one. Look for common error patterns across nodes — the same error on multiple CL nodes likely points to a shared cause (CL client bug, consensus rule issue).

   **If logs return nothing at all** for a node, that is itself a signal — but it does not necessarily mean the node is down (it may just not be shipping logs). Verify by querying the node directly via its localhost port (e.g. `/eth/v1/node/syncing`). If the node responds, it is running but not logging; if it is unreachable, it is truly down. Report either finding to the user.

5. **Fetch EL logs if CL points to execution issues** - You MAY investigate EL logs if CL logs show:
   - Engine API errors (e.g. `engine_newPayload` failures, timeouts)
   - Payload validation failures
   - Execution sync issues
   - "execution client unavailable" or similar

   If any appear, fetch EL logs for the same node.

   **CL/EL diagnostic matrix** — use this to narrow the root cause:
   - **Errors only in CL logs** → consensus issue (attestation bug, fork choice problem, CL client bug)
   - **CL engine API errors + EL errors** → execution issue (invalid block, state transition bug, EL client bug)
   - **CL clean but EL errors** → EL struggling but CL compensating; monitor but may not be primary cause
   - **Both layers erroring** → shared dependency (disk, memory, network) or cascading failure

6. **Escalate to WARN/INFO if needed** - If CRIT/ERR logs are empty or inconclusive at both CL and EL levels, broaden to WARN, then INFO. You MAY go to DEBUG as a last resort — DEBUG logs are very verbose and may time out.

7. **Correlate logs with baseline** - You SHOULD match log timestamps against the baseline data from Phase 1:
   - When did errors start relative to missed slots or participation drops?
   - Do errors correlate with a specific epoch or slot?
   - Are errors from one client type or spread across multiple?
   - Are the errors at the CL level, EL level, or both?
   - If the network has split, compare logs from nodes on different forks to find the divergence point

This concludes the **data collection phase**. You should now have: a baseline (from Dora or direct API queries), targeted CL logs (and EL logs if relevant) from the problematic nodes, and an understanding of which layer the errors originate from.

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
   - If a network split occurred, what is the first block where forks diverge? What is special about that block?
   - If you suspect a specific EIP is involved, and an EIP skill is available, use it to fetch the exact specification to confirm or rule out a faulty implementation.

   Append theories and reasoning to the debug report.

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
