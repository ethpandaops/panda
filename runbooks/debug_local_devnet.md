---
name: Debug Local Devnet
description: Collect information about a local Kurtosis devnet or systematically debug issues using Dora, OTel ClickHouse logs, and direct node API access to diagnose network splits, offline nodes, finality delays, and client bugs
tags: [devnet, debugging, network-split, forks, logs, consensus, validators, status, info, kurtosis, local, local devnet, enclave]
prerequisites: []
---

The first step in debugging a local devnet is discovering what tooling is available in the Kurtosis enclave, then gathering information from whatever is present. Not all local devnets have Dora or an OTel ClickHouse datasource — it depends on how the user configured their Kurtosis run and whether the local OTel stack is running. Phase 0 determines the data profile so the debug flow adapts accordingly.

**The user MUST specify which enclave to debug.** Do NOT assume an enclave — if the user hasn't specified one, ask them before proceeding. You can discover running enclaves with `kurtosis enclave ls`.

**Local devnets do NOT use the hosted ClickHouse datasources.** Start by capturing `panda datasources`, then only use the `local_kurtosis` ClickHouse log helper source (backed by the `local-kurtosis` datasource) when it is discovered. Do not use the hosted `clickhouse-raw`/`clickhouse-refined` datasources for local Kurtosis logs.

Refer to the query skill for general API usage patterns (Dora overview, ClickHouse queries, direct HTTP calls, Dora link generation, etc.). This runbook only covers the debugging-specific procedure and API calls not in the skill.

## How OTel Logs Flow

Kurtosis devnet services emit logs to the devnet's `otel-collector`. The collector writes them into the Kurtosis ClickHouse service on HTTP port `18123`, database `otel`, table `otel_logs`. Panda starts an in-process local proxy that autodiscovers this ClickHouse when `/ping` returns `Ok.` and the `otel` database exists, then exposes it as the `local-kurtosis` ClickHouse datasource. Use the `clickhouse.log_*` helpers with source `local_kurtosis` for normal investigation; they generate SQL that filters by `EnclaveName` because one local ClickHouse can hold logs from multiple devnets. If `local-kurtosis` is absent from the advertised datasources, treat ClickHouse logs as unavailable and use the `kurtosis service logs` fallback.

## Sandbox Session

Each `execute_python` / `panda execute` call spins up a **fresh** sandbox unless you pin one session — so `/workspace` (and the debug report) is empty each step, and you can hit the 10-session limit mid-investigation. **Create one session at the start, reuse it for every step, destroy it at the end:**

- **CLI:** `panda session create`, then pass `--session <id>` to every `panda execute`; `panda session destroy <id>` when done.
- **MCP:** create/select a session with `manage_session` and pass it to every `execute_python` call.

This is also what makes the `/workspace` debug report (next section) persist across steps.

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

**Appending to the debug report:** In every subsequent step, read the path from `/workspace/debug_file_path.txt` and append with `open(debug_file, "a")`. Do this for every piece of data collected — raw API responses, log extracts, summaries, and theories.

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

   Example datasource check. Also capture `panda datasources` outside the sandbox and append that raw output to the debug report so the user can verify which datasource names were advertised:
   ```python
   from ethpandaops import clickhouse

   clickhouse_datasources = clickhouse.list_datasources()
   clickhouse_names = [
       ds.get("name") if isinstance(ds, dict) else ds
       for ds in clickhouse_datasources
   ]
   has_otel_clickhouse = "local-kurtosis" in clickhouse_names
   print(f"has_otel_clickhouse={has_otel_clickhouse}")
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

**Dora is the primary starting point** when present in the enclave — start there whenever `has_dora = true`.

**Skip this phase if `has_dora = false`.** Instead, build a baseline from the nodes directly via their localhost ports: per CL node fetch `/eth/v1/node/syncing`, `/eth/v1/beacon/headers/head`, `/eth/v1/beacon/states/head/finality_checkpoints`; per EL node call `eth_blockNumber` + `eth_syncing`. Compare head slots/roots (splits) and finality, append to the report, then go to Phase 2.

**Dora-tolerance:** Dora's epoch/overview endpoints can return `HTTP 500: PANIC: ... integer divide by zero` exactly when a network is degraded (non-finalizing / zero-participation epochs hit a divide-by-zero). **Wrap every Dora call in try/except.** On a panic, note it (it corroborates near-zero participation / no finality) and fall through to the direct CL/EL baseline above for the failing calls, keeping any that succeeded.

**Read `sync_distance` explicitly** — it separates a split from a healthy spread. Wall-clock slot ≈ `head_slot + sync_distance`; take the max across nodes as the network wall-clock slot.
- All `is_syncing = false`, `sync_distance ≈ 0`, head slots within 1–2 → **healthy** (propagation spread, not a split).
- Most nodes stuck far back with large `sync_distance` while a few keep up → those nodes **can't follow the chain** (wedged at a divergence block) → split / stalled, not normal lag.
- `finalized` identical and far behind wall-clock on **all** nodes → **finality stalled** network-wide; the stall epoch (`finalized * 32`) is your divergence-centred timeframe.

1. **Collect all Dora data** - If Dora is available in the enclave, query it via its localhost port. In a single step, gather all network data and append raw responses to the debug report. You MAY combine these into one `execute_python` call:

   - **Network overview** — use `search(type="examples", query="network overview")` for the pattern. Note: `current_slot` is `epoch * 32` (epoch's first slot), not actual head slot.
   - **Network forks (split detection)** — **the reliable check is the node head-root sweep** (Phase 1 fallback below): compare `/eth/v1/beacon/headers/head` roots across CL nodes; divergent roots at the same slot mean a split. Dora `/forks` also reports forks, but the `search(type="examples", query="network splits")` httpx pattern only works if the local Dora port is reachable from your execution context — if it errors, fall back to the head-root sweep rather than assuming "no split." Healthy = one fork / one head root.
   - **Epoch details** — use `search(type="examples", query="epoch summary")`. Iterate through ~9 epochs per hour across the active timeframe. **Always start from head epoch - 1** (the most recent completed epoch) — the head epoch is still in progress and will show artificially low participation. You SHOULD also check the head epoch, but treat its data as preliminary since the epoch may not be finished — it is still useful for identifying offline proposers in recent slots. You SHOULD use try/except per epoch to handle failures without crashing.
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

   Append the baseline summary to the debug report as a readable narrative. If Dora is available, you SHOULD generate Dora links for relevant epochs, slots, and validators using the `dora.link_*()` helpers (see query skill).

   **If the baseline shows a healthy network** (no splits, finality on track, high participation, no offline nodes) but the user reports issues, present the healthy baseline to the user and ask them for more details about what they're observing. You MAY proceed to log investigation only if you have a specific target — otherwise let the user guide the next step.

## Phase 2: Log Investigation

### If OTel ClickHouse is available (`has_otel_clickhouse = true`)

Use the autodiscovered `local-kurtosis` ClickHouse datasource. The local OTel tables are shared by all active Kurtosis devnets, so **the first query MUST discover available enclaves** before querying logs for a specific devnet.

Useful schema fields:
- `otel.otel_logs`: `Timestamp DateTime64(9)`, `ServiceName LowCardinality(String)`, `Body String`, `SeverityText LowCardinality(String)`, `SeverityNumber UInt8`, `EnclaveName LowCardinality(String)`, `EnclaveUuid`, `ResourceAttributes Map(LowCardinality(String), String)`, `LogAttributes Map(LowCardinality(String), String)`

**Severity filters:** Always filter by `enclave` and usually `service`. This is ClickHouse SQL under the hood, not LogQL. Prefer the log helpers; `clickhouse.log_errors()` uses `SeverityNumber`, `SeverityText`, and `LogAttributes['level']` first, then falls back to a bounded ANSI-stripped `Body` regex for raw logs. Use `min_severity="warn"` only when you intentionally need WARN/WRN rows.

Run this once for the target service and append it to the debug report:

```python
from ethpandaops import clickhouse

enclave = "<enclave-name>"
service = "<service-name>"
timeframe = {"since": "1h"}  # replace with the active timeframe if different

coverage = clickhouse.log_coverage(
    "local_kurtosis",
    filters={"enclave": enclave, "service": service},
    include_sql=True,
    **timeframe,
)
print(coverage)
```

Use the active timeframe from Timeframe Rules. If you need raw SQL, search examples for the raw ClickHouse fallback and keep it bounded by `EnclaveName` + `ServiceName` + tight `Timestamp` window + `LIMIT`.

**FIRST: discover enclaves present in the OTel logs table**
```python
from ethpandaops import clickhouse

enclaves = clickhouse.log_values(
    "local_kurtosis",
    "enclave",
    since="1h",
    limit=50,
)
print(enclaves)
```

If the requested enclave is not listed, the OTel datasource is not currently receiving logs for that devnet. Fall back to `kurtosis service logs`.

**Discover services for the selected enclave**
```python
from ethpandaops import clickhouse

enclave = "<enclave-name>"
timeframe = {"since": "1h"}

services = clickhouse.log_values(
    "local_kurtosis",
    "service",
    filters={"enclave": enclave},
    limit=100,
    **timeframe,
)
print(services)
```

**Fetch recent CL errors for the selected enclave**
```python
from ethpandaops import clickhouse

enclave = "<enclave-name>"
timeframe = {"since": "1h"}

cl_errors = clickhouse.log_errors(
    "local_kurtosis",
    filters={"enclave": enclave},
    like_filters={"service": "cl-%"},
    limit=100,
    body_chars=240,
    include_sql=True,
    **timeframe,
)
for row in cl_errors["rows"]:
    print(row)
```

**Fetch recent error-class logs for a specific service**
```python
from ethpandaops import clickhouse

enclave = "<enclave-name>"
service = "<service-name>"
timeframe = {"since": "1h"}

service_logs = clickhouse.log_errors(
    "local_kurtosis",
    filters={"enclave": enclave, "service": service},
    limit=100,
    body_chars=240,
    include_sql=True,
    **timeframe,
)
for row in service_logs["rows"]:
    print(row)
```

**Fetch EL warnings/errors when CL logs point to execution issues**
```python
from ethpandaops import clickhouse

enclave = "<enclave-name>"
timeframe = {"since": "1h"}

el_logs = clickhouse.log_errors(
    "local_kurtosis",
    filters={"enclave": enclave},
    like_filters={"service": "el-%"},
    min_severity="warn",
    limit=100,
    body_chars=240,
    include_sql=True,
    **timeframe,
)
for row in el_logs["rows"]:
    print(row)
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

   Log level formats vary by client. In OTel logs, anchor on the LEVEL token (uppercase token or logfmt `level=error`) and exclude DEBUG/TRACE — use the anchored pattern shown above, not a bare `(?i)error` substring. If needed, add the WARN token to the pattern, then drop the severity filter for INFO-level terms.

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

   Append the summary to the debug report. You MUST provide the user with the file path.

## Key Thresholds

- Finality requires >66.7% (2/3) of stake attesting correctly
- Normal finality lag is 2 epochs (~13 minutes on mainnet, varies on devnets)
- >4 epochs without finality is cause for concern
- >8 epochs suggests a significant network issue
