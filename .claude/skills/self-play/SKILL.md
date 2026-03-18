---
name: self-play
description: Run schema probing self-play loop to find and fix ClickHouse schema ambiguity in the panda repo. Use when the user wants to improve query reliability by finding where the agent picks different tables for the same question.
---

# Self-Play Schema Probing

You are running the self-play loop for the ethpandaops/panda project. This finds schema ambiguities by asking the same question N times with different personas and checking if the generated queries agree on which tables to use. When they disagree, you present the options to the user, they pick the right answer, and you write the fix.

## Prerequisites

The panda repo must be at the working directory. The probe infrastructure lives in `tests/eval/`.

**Server**: Build first, then the probe runner auto-starts a local server on :2481:
   ```bash
   make build  # builds panda-server binary
   ```

**Dependencies**: The evaluator LLM needs `OPENROUTER_API_KEY` set in the environment.

## The Loop

### Step 1: Run probes

Run the probe runner. Start with a small batch to keep iteration fast:

```bash
cd tests/eval
uv run python -m scripts.run_probes --model claude-haiku-4-5 -n 10
```

The local server starts on :2481 and shuts down automatically when probes finish. First run takes ~10s for startup.

Read the latest results file from `tests/eval/probes/results/`.

### Step 2: Present disagreements

For each probe where `all_agreed` is false, present the disagreement to the user using `AskUserQuestion`. Always include the **full probe question** in the AskUserQuestion so the user has complete context without needing to look it up. Show:
- The full question that was asked (from probes.yaml)
- What each persona chose (which tables)
- Ask which approach is correct

**Triage scores before presenting:**
- **0.80 with 1 "(no tables)"**: Usually means one persona errored — not a real disagreement. Skip these unless the user asks about them.
- **0.60 and below**: Real schema ambiguity worth presenting.
- **1.00**: Fully agreed, no action needed.

Example:
```
**blob_mempool_getblobs_success**: "How does blob mempool propagation affect engine_getBlobs success rates on mainnet? What fraction of getBlobs calls return SUCCESS vs EMPTY?"

- default: fct_engine_get_blobs_by_slot, libp2p_gossipsub_blob_sidecar
- careful: blob_propagation_by_slot, fct_engine_get_blobs_by_slot, getblobs_by_slot, getblobs_status_by_slot, libp2p_gossipsub_blob_sidecar
- concise: fct_engine_get_blobs_by_slot, libp2p_gossipsub_blob_sidecar

Which tables should be used?
```

Let the user pick, or tell you the right answer.

### Step 3: Write the fix

Based on the user's answer, decide the best intervention. The entire repo is in scope — pick whatever will most effectively resolve the ambiguity. Find the root cause of why the model is confused rather than adding surface-level patches.

Possible fixes, in rough order of impact:

- **Examples** (`modules/clickhouse/examples.yaml`) — add a query example showing the correct table and pattern. Best for "which table do I use for X?" ambiguities.
- **Runbooks** (`runbooks/*.md`) — add or update a runbook with procedural guidance. Best for multi-step cross-cluster workflows.
- **Search tool / Python API docs** — if the model can't discover the right content, the platform itself might need changes (search behavior, tool descriptions, etc).
- **Schema comments** — if a table's purpose is unclear, the fix might be upstream in xatu-cbt, not here. Flag it.

For examples specifically:
- Put them in the appropriate category (create a new one if needed)
- Be clear about which cluster to use (`xatu` vs `xatu-cbt`)
- Include the partition key filter (`slot_start_date_time`) and network filter
- Use `{network}` placeholder for network name in CBT tables, or `meta_network_name` filter for xatu tables

Read existing files before modifying them.

**Important**: Fixes must generalize. Don't add a narrow example that only answers the exact probe question — add something that teaches the agent how to handle the whole class of questions. For example:
- Bad: "Maximum block size query" → only helps if someone asks exactly that
- Good: "Block properties (size, gas, tx count, value)" → helps any block property question pick the right table

The goal is that fixing one probe also fixes 5 others in the same domain that we haven't written yet.

**Tip**: Add clear positive examples that demonstrate the correct pattern. Never resort to "do NOT use X" negative guidance — that's lazy and doesn't teach anything.

### Step 4: Rebuild and re-run

After making fixes:
1. `make build` to rebuild the server
2. Re-run only the previously failing probes: `uv run python -m scripts.run_probes --model claude-haiku-4-5 --only-previously-failed`
3. Check if agreement improved

### Step 5: Repeat

Go back to Step 1 with more probes or different probes until the user is satisfied.

## Probe configuration

- Probe questions live in `tests/eval/cases/probes.yaml`
- Results accumulate as timestamped JSON files in `tests/eval/probes/results/`
- Use `--probe "glob_pattern"` to filter specific probes by ID (fnmatch syntax, single pattern only — no commas). Examples: `--probe "block_*"`, `--probe "mev_*"`
- Use `-n N` to limit how many probes to run
- Use `-v` for verbose output showing generated code
- Use `--only-previously-failed` to re-run only probes that disagreed in the last run

## Key files

- `tests/eval/cases/probes.yaml` — probe questions
- `tests/eval/scripts/run_probes.py` — probe runner
- `tests/eval/probes/analysis.py` — table extraction and agreement scoring
- `tests/eval/probes/results/` — timestamped result files
- `modules/clickhouse/examples.yaml` — where fixes go (query examples)
- `runbooks/*.md` — alternative fix target (procedural guides)
- `tests/eval/config-probe.yaml` — server config for local probe runs
