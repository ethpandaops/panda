---
name: self-play
description: Run schema probing self-play loop to find and fix ClickHouse schema ambiguity in the panda repo. Use when the user wants to improve query reliability by finding where the agent picks different tables for the same question.
---

# Self-Play Schema Probing

You are running the self-play loop for the ethpandaops/panda project. This finds schema ambiguities by asking the same question N times with different personas and checking if the generated queries agree on which tables to use. When they disagree, you present the options to the user, they pick the right answer, and you write the fix.

## Prerequisites

The panda repo must be at the working directory. The probe infrastructure lives in `tests/eval/`.

**Server**: Always use `--local-server` to run probes against a fresh local binary on :2481. This keeps the docker `panda-server` on :2480 untouched (it uses the published image, not local code). Build first:
   ```bash
   make build  # builds panda-server binary
   # The probe runner handles startup/shutdown with --local-server flag
   ```

**Dependencies**: The evaluator LLM needs `OPENROUTER_API_KEY` set in the environment.

## The Loop

### Step 1: Run probes

Run the probe runner. Start with a small batch to keep iteration fast:

```bash
cd tests/eval
uv run python -m scripts.run_probes --model claude-haiku-4-5 -n 10 --attempts 3 --local-server
```

The local server starts on :2481 and shuts down automatically when probes finish. First run takes ~10s for startup.

Read the latest results file from `tests/eval/probes/results/`.

### Step 2: Present disagreements

For each probe where `all_agreed` is false, present the disagreement to the user using `AskUserQuestion`. Show:
- The question that was asked
- What each persona chose (which tables)
- Ask which approach is correct

Example:
```
Question: "What is the maximum block size in bytes seen in the last hour on mainnet?"

Attempt 1 (default): canonical_beacon_block
Attempt 2 (careful): fct_prepared_block
Attempt 3 (concise): fct_prepared_block

Which table should be used for block size queries?
```

Let the user pick, or tell you the right answer.

### Step 3: Write the fix

Based on the user's answer, decide the best intervention. Everything in this repo is in scope — pick whatever will most effectively resolve the ambiguity:

- **Examples** (`modules/clickhouse/examples.yaml`) — add a query example showing the correct table and pattern. Best for "which table do I use for X?" ambiguities.
- **Runbooks** (`runbooks/*.md`) — add or update a runbook with procedural guidance. Best for multi-step investigative workflows.
- **Python API docs** — update module docstrings/descriptions if the Python API itself is misleading.
- **Getting-started snippets** — update per-module guidance if the agent is missing basic context.
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

**Tip**: Include negative guidance in category descriptions (e.g. "Use fct_block_head — NOT canonical_execution_block, fct_prepared_block"). This steers the model away from wrong tables effectively.

### Step 4: Rebuild and re-run

After adding examples:
1. `make build` to rebuild the server with new examples
2. Re-run only the previously failing probes: `uv run python -m scripts.run_probes --model claude-haiku-4-5 --only-previously-failed --local-server`
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
