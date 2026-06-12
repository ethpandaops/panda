# ethpandaops-panda Evaluation Harness

One harness, two launch modes. Cases live in `cases/*.yaml` (one domain per file, selected
by tags) and are graded by [promptfoo](https://promptfoo.dev) `assert:` blocks (llm-rubric);
a coding-agent subject (opencode driving the `panda` CLI) answers each case against live
data.

- **`scripts.eval`** — run a case selection once, print a table, write JUnit XML, exit
  nonzero on failure. This is what CI runs.
- **`scripts.harden`** — the *same* measurement core wrapped in an optimization loop: a
  proposer (Codex) edits the panda harness from the raw traces, re-measures, and keeps the
  change only if it doesn't regress correctness and is bootstrap-confidently more efficient.

Both call `harness.promptfoo_eval.measure_candidate` — the difference is just launch params.
The split is deliberate: `harness/` is the shared measurement core every entry point uses;
`harden/` is the optimization loop built on top of it.

## Quick Start

```bash
cd tests/eval
uv sync                  # Python deps
# promptfoo runs via `npx promptfoo@latest` (needs Node); the agent needs opencode auth.

# single-pass eval: tags select cases (no flags = every case in cases/*.yaml)
uv run python -m scripts.eval --tags smoke
uv run python -m scripts.eval                      # the whole suite
uv run python -m scripts.eval --tags mev,blobs --subject opencode-go/deepseek-v4-flash:cli

# build + run a local scratch server from the current source, then eval against it
uv run python -m scripts.eval --tags smoke --scratch

# the optimization loop (throwaway worktree/branch — it commits/reverts)
uv run python -m scripts.harden --rounds 3

# interactive sandbox REPL
uv run python -m scripts.repl
```

Required environment:
- `OPENCODE_GO_API_KEY` — the agent subject (opencode-go provider).
- `OPENROUTER_API_KEY` — the promptfoo grader (`--judge-model` → `openrouter:<model>`).
- A reachable panda server: CI starts one; locally use `--scratch`, or point your
  `~/.config/panda/config.yaml` at a running server.

## Cases

Files under `cases/` are purely organizational — one domain per file (`smoke.yaml`,
`blocks.yaml`, `mev.yaml`, `validators.yaml`, ...) with ids unique across the whole
directory. Selection is by tags: `--tags smoke` runs the smoke cases wherever they live,
no flags runs everything, `--cases <file>` restricts to one file when iterating on it.
There is no hand-maintained "all" file — the union of `cases/*.yaml` *is* the suite, so
adding a case to any file automatically adds it to the full runs (release qualification,
`scripts.harden` defaults).

The suite is deliberately diverse — cases pressure-test DIFFERENT parts of the harness,
not more variants of one question. That breadth is what makes the harden loop's held-out
gate and token-bloat penalty bite: a change that stuffs the always-loaded clickhouse docs
costs the unrelated questions tokens, and overfitting one datasource won't help the others.

A case is single- or multi-turn and carries its own grading rubric. No method assertions
(no expected-tools / expected-tables) — the rubric describes what a *correct answer* looks
like; token efficiency is scored separately, so wasteful paths are penalised without being
asserted. The proposer never sees the rubrics (only question text + traces), so rubrics
can be specific about the expected answer without leaking the method into the harness.

```yaml
# single-turn
- id: mev_relay_share
  input: "What share of mainnet blocks over the last 24h were delivered via an MEV relay?"
  tags: [clickhouse, blocks, mev, relay]
  assert:
    - type: llm-rubric
      value: >
        Reports the share of recent mainnet blocks delivered via an MEV relay, from a
        real query. The true figure is high — roughly 85-95% — so an answer in that band
        passes; a missing percentage or one far outside it fails.

# multi-turn: steps run in one session; the rubric grades the whole transcript
- id: block_timing_analysis
  steps:
    - prompt: "Query block arrival times for the last 100 slots on mainnet."
    - prompt: "Group by observer consensus client; mean + p95 arrival per client."
    - prompt: "Create a box plot comparing the arrival distributions across clients."
  assert:
    - type: llm-rubric
      value: >
        Breaks arrival times down per consensus client with mean + p95, and produces a
        box plot (must provide the storage URL of the uploaded chart).
```

The loader flattens `steps` into `input` + `followups`; multi-turn runs share one session
and the grader sees a per-turn transcript (each turn tagged with its session id).

## How a run works

```
cases/*.yaml
   │  load_test_cases → Question(input, followups, asserts)
   ▼
harness.promptfoo_eval.measure()           # builds a promptfoo config, runs `npx promptfoo eval --repeat k`
   │  promptfoo/provider.py  (call_api)    # per case: runs the opencode subject in one session
   │     harness/subject.py  OpencodeSubject.run(prompts) → RunTrace (output transcript + full tool calls)
   │  promptfoo grades each case's llm-rubric assert via the judge model
   ▼
harness.scoring  # 0 if wrong, else efficiency(tokens); the loop's gates live here
   ▼
CandidateResult  # + full untruncated traces written to run_dir/traces/ for humans/the proposer
```

`scripts.eval` reports + emits JUnit/JSON and exits. `scripts.harden` feeds the traces to
the proposer and loops.

## Layout

```
tests/eval/
├── cases/            # one *.yaml per domain (selection by tags) + loader.py
├── promptfoo/        # provider.py — the promptfoo↔agent bridge
├── harness/          # shared measurement core (every entry point measures through this)
│   ├── promptfoo_eval.py   # build config, run promptfoo, parse → scored runs
│   ├── subject.py          # OpencodeSubject: runs the agent, returns a RunTrace
│   └── scoring.py          # the objective + acceptance gates
├── harden/           # the optimization loop on top of the harness
│   ├── loop.py             # measure → propose → re-measure → gate
│   ├── proposer.py         # Codex proposer
│   ├── auditor.py          # adversarial overfit/placement auditor
│   └── report.py           # the lean proposer prompt (+ on-disk traces)
├── agent/            # opencode agent wrapper (+ Langfuse trace push)
├── config/           # settings
├── scripts/
│   ├── eval.py             # single-pass eval (CI)
│   ├── harden.py           # optimization loop
│   ├── _panda_env.py       # scratch-server build/run
│   ├── ci_auth.py          # mint the panda-ci service-account token
│   ├── repl.py             # interactive sandbox REPL
│   └── langfuse.py         # local Langfuse docker-compose helper
└── tests/            # harness + harden unit tests
```

## CI

- **`eval-smoke.yaml`** — every PR + master push. A couple of fast cases against the hosted
  production proxy (as the `panda-ci` service account). Runs `scripts.eval --tags smoke`,
  publishes a check run, and posts one sticky PR comment (report link + per-question
  results + Langfuse trace links). Each run also gets the interactive
  report: `scripts.ci_report` builds the release-style page for the commit, with history
  assembled from the branch's previous smoke runs, the latest master run, and the most
  recent release record (restricted to the smoke questions). `scripts.ci_pages` publishes
  the payload JSON to `eval/ci/` on gh-pages, where one shared copy of the report page
  fetches per-commit payloads — the branch/commit switcher walks runs without re-shipping
  the UI. Size stays bounded everywhere: a view fetches the manifest plus one payload
  (tens of KB), payloads embed at most `MAX_BRANCH_HISTORY` comparison records, pruning
  caps runs per branch and expires idle branches, and every publish force-pushes gh-pages
  as a single parentless snapshot commit so deleted payloads don't pile up in git history.
  Fork PRs can't push gh-pages; their report ships only as the self-contained
  `eval-report.html` in the run artifact.
- **`eval.yaml`** — opt-in (the `run-evals` PR label, manual dispatch). Runs the whole
  suite (narrow with the `tags`/`cases` dispatch inputs) against the hosted production
  proxy (as `panda-ci`), same data plane as the smoke.
- **`release-eval.yaml`** — every `v*` tag (releases and `-rc.N` pre-releases). Single pass
  over every case (variations included) against the hosted proxy, then
  `scripts.release_scorecard` splices a scorecard into the GitHub release description:
  headline pass-rate/score, per-question flips vs the previous qualified release, a trend
  chart, and Langfuse trace links. Each run's `eval-qualification.json` is attached as a
  release asset — that's the history future runs compare against. It's a human-reviewed
  scorecard, not a gate: cut a `vX.Y.Z-rc.N` tag (published as a GitHub pre-release, so
  `panda upgrade` users never see it), read the scorecard, then push the final tag.

## Langfuse

When `MCP_EVAL_LANGFUSE_*` keys are set, the agent pushes each run's trace to Langfuse
automatically (the provider flushes after every run). That's the whole integration — humans
inspect the traces in Langfuse production; the harness scores and gates purely on the
returned trace. `scripts.langfuse {up,down,logs,status}` runs a local Langfuse via Docker
for development.

## Troubleshooting

- **`OPENCODE_GO_API_KEY ... must be set`** — export it before running (the agent guards on
  it). CI sources it from a secret into `~/.local/share/opencode/auth.json` + the env.
- **`promptfoo produced no results`** — Node/`npx` missing, or the worker couldn't import the
  agent stack; `scripts.eval`/`scripts.harden` set `PROMPTFOO_PYTHON` to the active venv.
- **Server not ready** — the agent needs a reachable panda server; use `--scratch` locally.
