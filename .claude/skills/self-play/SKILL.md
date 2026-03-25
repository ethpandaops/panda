---
name: self-play
description: Run schema probing self-play loop to find and fix ClickHouse schema ambiguity in the panda repo. Use when the user wants to improve query reliability by finding where the agent picks different tables for the same question.
---

# Self-Play Schema Probing

You are running the self-play loop for the ethpandaops/panda project. This finds schema ambiguities by asking the same question N times with different personas and checking if the generated queries agree on which tables to use. When they disagree, you use schema introspection to determine the correct tables and write the fix autonomously.

The primary metric is **average entropy** across all probes. Lower is better (0 = perfect agreement). This number should trend down over time as you add examples and runbooks.

## Prerequisites

The panda repo must be at the working directory. The probe infrastructure lives in `tests/eval/`.

**Server**: Build first, then the probe runner auto-starts a local server on :2481:
   ```bash
   make build  # builds panda-server binary
   ```

**Dependencies**: The evaluator LLM needs `OPENROUTER_API_KEY` set in the environment.

## The Loop

### Step 1: Run probes

Run all probes:

```bash
cd tests/eval
uv run python -m scripts.run_probes --model claude-haiku-4-5
```

To filter by domain: `--tag blobs`, `--tag mev`, `--tag attestations`, etc.

The local server starts on :2481 and shuts down automatically when probes finish. First run takes ~10s for startup.

Read the latest results file from `tests/eval/probes/results/`.

### Step 2: Resolve disagreements via schema introspection

For each probe where `all_agreed` is false, **resolve it yourself using the actual schema**:

1. Collect all candidate tables from all personas
2. Run `./panda schema <table>` for each candidate to get columns, types, and comments
3. Compare the schemas against the probe question — which table(s) actually have the columns needed to answer it?
4. Determine the correct table set based on schema evidence
5. If a persona chose a table that doesn't exist in `./panda schema`, it hallucinated — discard it
6. If multiple real tables could work, prefer: fact tables (`fct_`) over canonical tables, pre-aggregated over raw, xatu-cbt over xatu for performance

**Only escalate to the user if** the schema genuinely doesn't disambiguate — e.g., two tables have overlapping columns and it's unclear which is the right source of truth for the question.

**Skip these:**
- Probes where entropy = 0 (all agreed)
- Probes where one persona errored ("(no tables)") and the rest agree — the error is not a schema problem

### Step 3: Write the fix

Based on your schema analysis, decide the best intervention. The entire repo is in scope — pick whatever will most effectively resolve the ambiguity. Find the root cause of why the model is confused rather than adding surface-level patches.

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
- Use hardcoded literal values (block numbers, slots) in examples — never subqueries like `SELECT max(block_number) FROM ...` that cause full table scans

Read existing files before modifying them.

**Important**: Fixes must generalize. Don't add a narrow example that only answers the exact probe question — add something that teaches the agent how to handle the whole class of questions. For example:
- Bad: "Maximum block size query" → only helps if someone asks exactly that
- Good: "Block properties (size, gas, tx count, value)" → helps any block property question pick the right table

The goal is that fixing one probe also fixes 5 others in the same domain that we haven't written yet.

**Tip**: Add clear positive examples that demonstrate the correct pattern. Never resort to "do NOT use X" negative guidance — that's lazy and doesn't teach anything.

### Step 4: Rebuild and re-run by domain

After making fixes:
1. **Commit the fix** as an atomic commit (one fix per commit) so it can be reverted independently if needed
2. `make build` to rebuild the server
3. Identify the domain tags of the fixed probes (from `probes.yaml`)
4. Re-run the entire domain to test generalization: `uv run python -m scripts.run_probes --model claude-haiku-4-5 -c 20 --tag <domain>`
5. Check if average entropy improved — both for the fixed probes and for related probes in the same domain
6. **Evaluate the results**: If entropy regressed on other probes, decide whether the regression is caused by your fix or is just noise (LLM variance). Consider:
   - Did the target probe improve? By how much?
   - Did other probes in the same domain regress? Or unrelated probes?
   - Is the regression small (0.72 → 0.72 fluctuation) or large (0.00 → 1.52)?
   - If the fix clearly caused harm, `git revert <commit>` and try a different approach
   - If the fix helped the target and regressions look like noise, keep going

### Step 5: Repeat

Go back to Step 1. The goal is to drive average entropy toward zero across all 38 probes.

## Probe configuration

- Probe questions live in `tests/eval/cases/probes.yaml`
- Results accumulate as timestamped JSON files in `tests/eval/probes/results/`
- Use `--probe "glob_pattern"` to filter specific probes by ID (fnmatch syntax, single pattern only — no commas). Examples: `--probe "block_*"`, `--probe "mev_*"`
- Use `--tag <tag>` to filter probes by domain tag (e.g., `--tag blobs`, `--tag mev`)
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
