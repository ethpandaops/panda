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

Based on the user's answer, add a targeted example to `modules/clickhouse/examples.yaml` that clearly shows the correct table and query pattern for that question domain. The example should be:
- In the appropriate category (create a new one if needed)
- Clear about which cluster to use (`xatu` vs `xatu-cbt`)
- Include the partition key filter (`slot_start_date_time`) and network filter
- Use `{network}` placeholder for the network name in CBT tables, or `meta_network_name` filter for xatu tables

Read the existing `modules/clickhouse/examples.yaml` to understand the format before adding.

**Tip**: Include negative guidance in category descriptions (e.g. "Use fct_block_head — NOT canonical_execution_block, fct_prepared_block"). This steers the model away from wrong tables effectively. A single well-placed category can fix multiple related probes.

### Step 4: Rebuild and re-run

After adding examples:
1. `make build` to rebuild the server with new examples
2. Re-run the probes for the fixed questions: `uv run python -m scripts.run_probes --model claude-haiku-4-5 --probe "the_fixed_probe_id" --attempts 3 --local-server`
3. Check if agreement improved

### Step 5: Repeat

Go back to Step 1 with more probes or different probes until the user is satisfied.

## Probe configuration

- Probe questions live in `tests/eval/cases/probes.yaml`
- Results accumulate as timestamped JSON files in `tests/eval/probes/results/`
- Use `--probe "glob_pattern"` to filter specific probes by ID (fnmatch syntax, single pattern only — no commas). Examples: `--probe "block_*"`, `--probe "mev_*"`
- Use `-n N` to limit how many probes to run
- Use `-v` for verbose output showing generated code

## Key files

- `tests/eval/cases/probes.yaml` — probe questions
- `tests/eval/scripts/run_probes.py` — probe runner
- `tests/eval/probes/analysis.py` — table extraction and agreement scoring
- `tests/eval/probes/results/` — timestamped result files
- `modules/clickhouse/examples.yaml` — where fixes go (query examples)
- `runbooks/*.md` — alternative fix target (procedural guides)
- `tests/eval/config-probe.yaml` — server config for local probe runs
