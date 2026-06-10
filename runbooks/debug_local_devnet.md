---
name: Debug Local Devnet
description: Collect information about a local Kurtosis devnet or systematically debug issues using Dora, OTel ClickHouse logs, and direct node API access to diagnose network splits, offline nodes, finality delays, and client bugs
tags: [devnet, debugging, network-split, forks, logs, consensus, validators, status, info, kurtosis, local, local devnet, enclave]
prerequisites: []
---

The first step in debugging a local devnet is discovering what tooling is available in the Kurtosis enclave, then gathering information from whatever is present. Not all local devnets have Dora or an OTel ClickHouse datasource — it depends on how the user configured their Kurtosis run and whether the local OTel stack is running. Phase 0 determines the data profile so the debug flow adapts accordingly.

**The user MUST specify which enclave to debug.** Do NOT assume an enclave — if the user hasn't specified one, ask them before proceeding. You can discover running enclaves with `kurtosis enclave ls`.

**Local devnets do NOT use the hosted ClickHouse datasources.** For logs, only use `clickhouse.query("local-kurtosis", ...)` when the `local-kurtosis` ClickHouse datasource is discovered. Do not use the hosted `clickhouse-raw`/`clickhouse-refined` datasources for local Kurtosis logs.

Refer to the query skill for general API usage patterns (Dora overview, ClickHouse queries, direct HTTP calls, Dora link generation, etc.). This runbook only covers the debugging-specific procedure and API calls not in the skill.

## How OTel Logs Flow

Kurtosis devnet services emit logs to the devnet's `otel-collector`. The collector writes them into the Kurtosis ClickHouse service on HTTP port `18123`, database `otel`, table `otel_logs`. Panda starts an in-process local proxy that autodiscovers this ClickHouse when `/ping` returns `Ok.` and the `otel` database exists, then exposes it as the `local-kurtosis` ClickHouse datasource. Query it with SQL, always filtering by `EnclaveName` because one local ClickHouse can hold logs from multiple devnets.

## Debug Report

At the start of each debug session, create a single file at `/workspace/<network>-debug-<timestamp>.md`. Append ALL raw API responses, log extracts, and analysis notes to this file as you go. At the end of the session, provide the user with the file path.

Initialize the file and a helper for appending:
```python
from datetime import datetime
import json
import os
from ethpandaops import storage

network = "<enclave-name>"
timestamp = datetime.utcnow().strftime("%Y%m%d-%H%M%S")
debug_file = f"/workspace/{network}-debug-{timestamp}.md"

with open(debug_file, "w") as f:
    f.write(f"# Debug Report: {network}\n")
    f.write(f"**Generated:** {datetime.utcnow().isoformat()}Z\n\n")

# Save the path for subsequent steps
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
    url = storage.upload(debug_file, remote_name=remote_name)
    with open("/workspace/debug_file_url.txt", "w") as f:
        f.write(url)
    append_debug("Debug Report URL", url)
    storage.upload(debug_file, remote_name=remote_name)
    return url
```

**Appending to the debug report:** In every subsequent step, read the path from `/workspace/debug_file_path.txt` and append with `open(debug_file, "a")`. Do this for every piece of data collected — raw API responses, log extracts, summaries, and theories.

**Minimum debug output contract:** Every collection step MUST append the exact query/API path/command used, the raw response or DataFrame before summarizing it, the active timeframe or slot/epoch window, any `data_quality_warnings` or source inconsistencies, and a short interpretation clearly labeled as interpretation.

## Timeframe Rules

All steps in this runbook MUST use the same consistent timeframe OR there must be a reason to change the timeframe. Determine the **active timeframe** once and use it everywhere. If you update the **active timeframe** mid debugging, then mention it in the raw dump:

1. If the user provides a specific timeframe or epoch range → use that
2. If a network split is detected in step 1 → override to the divergence slot/epoch and investigate around that point (before and after)
3. Otherwise → default to the **past 1 hour**

## Search-First Principle

Use the `search` tool to find relevant patterns, related procedures, and protocol context throughout this runbook:

- **Examples** — Phase 1 already references specific example searches. In other phases, if you need a query pattern you don't have (e.g., Prometheus metrics for node health, EL-specific queries), search for it rather than guessing: `search(type="examples", query="<what you need>")`
- **Runbooks** — If you hit a sub-problem during debugging that feels like it deserves its own procedure (e.g., "finality stalled but no split", "single client type failing"), check whether a dedicated runbook exists: `search(type="runbooks", query="<sub-problem>")`
- **EIPs** — When investigating protocol-level behavior, fork boundary issues, or suspected consensus rule edge cases, look up the relevant specification: `search(type="eips", query="<EIP topic or number>")`

This does NOT replace the hardcoded patterns in this runbook — those encode debug-specific knowledge (OTel table schema, error-level filtering, Kurtosis enclave/service fields, log field fallbacks, etc.) that generic examples won't have. Use search to fill gaps, not to replace what's already here.

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
   - **OTel ClickHouse** (logs): check panda's discovered ClickHouse datasources for a datasource named `local-kurtosis`. If present, `has_otel_clickhouse = true`. Do NOT probe local logging ports for this flow.
   - **Prometheus** (metrics): look for services containing `prometheus` in the enclave inspect output. If present, note its port.
   - Any other observability or debugging services the user may have included.

   Example datasource check:
   ```python
   from ethpandaops import clickhouse

   clickhouse_datasources = clickhouse.list_datasources()
   clickhouse_names = [
       ds.get("name") if isinstance(ds, dict) else ds
       for ds in clickhouse_datasources
   ]
   has_otel_clickhouse = "local-kurtosis" in clickhouse_names
   print(clickhouse_datasources)
   ```

   **Service status:** confirm all services are RUNNING. Services not in RUNNING state are already a finding — note them in the debug report.

   Record the **data profile** in the debug report:
   - `has_dora: true/false`
   - `has_otel_clickhouse: true/false`
   - Enclave name
   - List of CL/EL/VC services with their localhost ports
   - List of tooling services with their ports

   **Routing rules:**
   - `has_dora = true` → Phase 1 (Dora) runs normally.
   - `has_dora = false` → Use direct CL/EL API queries to build a baseline instead (see Phase 1 fallback below).
   - `has_otel_clickhouse = true` → Phase 2 uses the `local-kurtosis` ClickHouse datasource for OTel log investigation.
   - `has_otel_clickhouse = false` → Phase 2 falls back to `kurtosis service logs`.

## Phase 1: Data Collection with Dora

**Skip this phase if Phase 0 determined `has_dora = false`.** Instead, build a baseline by querying the CL and EL nodes directly via their localhost ports from enclave inspect. For each CL node, fetch `/eth/v1/node/syncing`, `/eth/v1/beacon/headers/head`, and `/eth/v1/beacon/states/head/finality_checkpoints`. For each EL node, call `eth_blockNumber` and `eth_syncing` via JSON-RPC. Compare head slots/roots across nodes to detect splits, and check finality checkpoints. Append results to the debug report, then proceed to Phase 2.

1. **Collect all Dora data** - If Dora is available in the enclave, query it via its localhost port. In a single step, gather all network data and append raw responses to the debug report. You MAY combine these into one `execute_python` call:

   - **Network overview** — use `search(type="examples", query="network overview")` for the pattern. `current_slot` is the latest indexed head slot; `current_epoch_start_slot` is the epoch boundary (`current_epoch * slots_per_epoch`, using the network's reported value). Dump the full overview object verbatim, including `finalized_epoch`, `epochs_since_finality`, `finalizing`, and any `data_quality_warnings`. Treat checkpoint-derived fields as the finality baseline; do not use Dora vote/participation aggregates to decide finality.
   - **Network forks** — use `search(type="examples", query="network splits")`. Query the Dora `/forks` endpoint (with `Accept: application/json` header) to detect splits. A healthy network has one fork.
   - **Epoch details** — use `search(type="examples", query="epoch summary")`. Iterate through ~9 epochs per hour across the active timeframe. **Always start from head epoch - 1** (the most recent completed epoch) — the head epoch is still in progress and will show artificially low participation. You SHOULD also check the head epoch, but treat its data as preliminary since the epoch may not be finished — it is still useful for identifying offline proposers in recent slots. You SHOULD use try/except per epoch to handle failures without crashing. Append each raw epoch payload before extracting fields. If an epoch marked finalized reports participation below 66.7%, flag it as a Dora data inconsistency instead of concluding the network finalized below threshold.
   - **Missing proposers** — use `search(type="examples", query="missing proposers")`. Adjust `slot_lookback` to match the active timeframe (~300 slots per hour).
   - **Offline attesters** — use `search(type="examples", query="offline attesters")`.

   If there are multiple forks:
   - **IMPORTANT:** A network split overrides the active timeframe. You MUST identify the divergence slot/epoch where the split occurred and refocus the entire investigation around that point. All subsequent steps MUST use this divergence-centered timeframe.
   - Participation and proposer data from Dora reflects the canonical fork — nodes on a minority fork will appear "offline" even though they may be running fine on their fork
   - The root cause investigation should focus on **why the split happened** rather than individual node failures
   - When checking logs later, compare logs from nodes on different forks to find the divergence point

2. **Build a baseline summary** - You MUST summarize the network state before proceeding to log investigation:
   - Is the network on a single fork or has it split? (if split, this likely explains most other symptoms)
   - Is the network finalizing? Use `finalized_epoch`, `epochs_since_finality`, and direct CL checkpoint evidence where available. How many epochs behind?
   - What independent participation evidence is available? (>66.7% required for finality) **Use the last completed epoch, not the head epoch**. State the source of the number. If the only available number is a Dora aggregate that contradicts checkpoint finality or has warnings, report it as unreliable instead of using it as the baseline.
   - Are there missed slots or empty epochs?
   - Which specific nodes/validators are offline or underperforming?
   - If there are multiple forks, which nodes are on which fork?
   - Which sources disagree, if any? For example: finalized checkpoint advancing while Dora epoch participation is below 66.7%, Dora service list vs Kurtosis services, or Dora forks vs direct RPC head roots.

   Append the baseline summary to the debug report as a readable narrative. If Dora is available, you SHOULD generate Dora links for relevant epochs, slots, and validators using the `dora.link_*()` helpers (see query skill).

   **If the baseline shows a healthy network** (no splits, finality on track, high participation, no offline nodes) but the user reports issues, present the healthy baseline to the user and ask them for more details about what they're observing. You MAY proceed to log investigation only if you have a specific target — otherwise let the user guide the next step.

## Phase 2: Log Investigation

### If OTel ClickHouse is available (`has_otel_clickhouse = true`)

Use the autodiscovered `local-kurtosis` ClickHouse datasource. The local OTel tables are shared by all active Kurtosis devnets, so **the first query MUST discover available enclaves** before querying logs for a specific devnet.

Useful schema fields:
- `otel.otel_logs`: `Timestamp DateTime64(9)`, `ServiceName LowCardinality(String)`, `Body String`, `SeverityText LowCardinality(String)`, `SeverityNumber UInt8`, `EnclaveName LowCardinality(String)`, `EnclaveUuid`, `ResourceAttributes Map(LowCardinality(String), String)`, `LogAttributes Map(LowCardinality(String), String)`

**Always filter by `EnclaveName` once you know it.** For service-level log queries, also filter by `ServiceName`. The Kurtosis OTel collector may leave `SeverityText` and `SeverityNumber` empty, so severity triage must use `match(Body, ...)` on the raw log line. Use the same active timeframe established in the Timeframe Rules section above.

**FIRST: discover enclaves present in the OTel logs table**
```python
from ethpandaops import clickhouse

enclaves = clickhouse.query("local-kurtosis", """
    SELECT DISTINCT EnclaveName
    FROM otel.otel_logs
    WHERE EnclaveName != ''
    ORDER BY EnclaveName
""")
print(enclaves)
```

If the requested enclave is not listed, the OTel datasource is not currently receiving logs for that devnet. Fall back to `kurtosis service logs`.

**Discover services for the selected enclave**
```python
from ethpandaops import clickhouse

enclave = "<enclave-name>"

services = clickhouse.query("local-kurtosis", """
    SELECT
      ServiceName,
      count() AS log_count,
      min(Timestamp) AS first_seen,
      max(Timestamp) AS last_seen
    FROM otel.otel_logs
    WHERE EnclaveName = {enclave:String}
      AND Timestamp >= now() - INTERVAL 1 HOUR
    GROUP BY ServiceName
    ORDER BY ServiceName
""", parameters={"enclave": enclave})
print(services)
```

**Fetch recent CL errors for the selected enclave**
```python
from ethpandaops import clickhouse

enclave = "<enclave-name>"

cl_errors = clickhouse.query("local-kurtosis", """
    SELECT
      Timestamp,
      ServiceName,
      Body
    FROM otel.otel_logs
    WHERE EnclaveName = {enclave:String}
      AND ServiceName LIKE 'cl-%'
      AND match(Body, '(?i)(crit|err|error|fatal)')
      AND Timestamp >= now() - INTERVAL 1 HOUR
    ORDER BY Timestamp DESC
    LIMIT 200
""", parameters={"enclave": enclave})
print(cl_errors)
```

**Fetch recent error-class logs for a specific service**
```python
from ethpandaops import clickhouse

enclave = "<enclave-name>"
service = "<service-name>"

service_logs = clickhouse.query("local-kurtosis", """
    SELECT
      Timestamp,
      ServiceName,
      Body
    FROM otel.otel_logs
    WHERE EnclaveName = {enclave:String}
      AND ServiceName = {service:String}
      AND match(Body, '(?i)(crit|err|error|fatal)')
      AND Timestamp >= now() - INTERVAL 1 HOUR
    ORDER BY Timestamp DESC
    LIMIT 200
""", parameters={"enclave": enclave, "service": service})
print(service_logs)
```

**Fetch EL warnings/errors when CL logs point to execution issues**
```python
from ethpandaops import clickhouse

enclave = "<enclave-name>"

el_logs = clickhouse.query("local-kurtosis", """
    SELECT
      Timestamp,
      ServiceName,
      Body
    FROM otel.otel_logs
    WHERE EnclaveName = {enclave:String}
      AND ServiceName LIKE 'el-%'
      AND match(Body, '(?i)(crit|err|error|fatal|warn)')
      AND Timestamp >= now() - INTERVAL 1 HOUR
    ORDER BY Timestamp DESC
    LIMIT 200
""", parameters={"enclave": enclave})
print(el_logs)
```

### If OTel ClickHouse is not available — fallback to kurtosis service logs

Use `kurtosis service logs` only when the `local-kurtosis` ClickHouse datasource is unavailable or does not contain the selected enclave:
- `kurtosis service logs <enclave> <service>` — logs for a specific service (default: last 200 lines)
- `kurtosis service logs <enclave> -x` — logs for **all** services
- `--regex-match "<pattern>"` — filter lines matching a regex (re2 syntax)
- `--match "<string>"` — filter lines containing a literal string
- `-n <count>` — number of lines to retrieve
- `-v` — invert the filter (lines NOT matching)
- `-a` — get all logs

### Log investigation procedure

Regardless of which log source is used, follow this procedure:

**You SHOULD start with the consensus layer (CL).** Most devnet issues originate at the CL level. Only investigate EL logs if CL logs point to execution-side problems (e.g. payload validation errors, engine API failures).

3. **Discover OTel enclave and service coverage** - If using OTel ClickHouse, first query `SELECT DISTINCT EnclaveName FROM otel.otel_logs`, then query service coverage for the selected enclave. Append results to the debug report.

4. **Fetch CL logs first (CRIT/ERR)** - For each problematic node (or all CL clients if no specific targets), query CL logs at the most severe log levels.

   Log level formats vary by client. In OTel logs, start with `match(Body, '(?i)(crit|err|error|fatal)')`; if needed, broaden the Body pattern to include `warn`, then INFO-level terms.

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
   - If you suspect a specific EIP is involved, use `search(type="eips", query="<EIP topic or number>")` to fetch the specification and confirm or rule out a faulty implementation.

   Append theories and reasoning to the debug report.

9. **Summarize findings** - You MUST present the user with:
   - A clear description of what is happening (symptoms)
   - The most likely root cause and supporting evidence
   - Which nodes/clients are affected
   - Dora links for relevant slots, epochs, and validators (if Dora was available)
   - Suggested next steps (e.g. restart a node, report a client bug, check infrastructure)

   Append the summary to the debug report. Then call `publish_debug_report()` and provide the user with the full fetchable URL, for example `http://localhost:2480/api/v1/storage/files/<execution-id>/<enclave>-debug-<timestamp>.md`. Also include the local `/workspace/...` path as secondary context.

## Key Thresholds

- Finality requires >66.7% (2/3) of stake attesting correctly
- Normal finality lag is 2 epochs (~13 minutes on mainnet, varies on devnets)
- >4 epochs without finality is cause for concern
- >8 epochs suggests a significant network issue
