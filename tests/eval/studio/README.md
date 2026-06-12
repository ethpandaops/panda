# panda case studio

Web UI for growing the eval suite from real agent behavior: ask a question, N
sandboxed opencode agents answer it via the local panda, a human reviews the
full tool-call traces and ticks verdicts, and the outcome ships as a PR —
either a new eval case (mint flow) or a codex-authored harness fix that was
adversarially audited and re-verified against a scratch build (fix flow).

## Run

```bash
make studio        # from the repo root → http://127.0.0.1:2499
```

Startup runs a preflight and fails early with the fix for anything missing:
opencode (+ opencode-go key), codex (+ login), gh auth, OPENROUTER_API_KEY
(judge for auto-triage/auto-draft), a running docker daemon, a panda-server on
:2480 (`docker compose up -d`), and the go toolchain.

## Flows

**Mint a case**: tick ✓ on correct runs → ✨ auto-draft writes id/description/
tags/rubric in house style → `mint case → open PR` appends the case to the
chosen `tests/eval/cases/*.yaml` in a throwaway worktree and opens a minimal PR.

**Work on a fix**: describe what's broken (+ optional hints/expected) →
codex finds the root cause in a fresh worktree off origin/master → deterministic
guards + adversarial audit (answer-leakage / misplacement / infra-gaming, amend
loop, fail-closed) → build + lint → the question re-runs against a scratch
server built from the patched tree → human reviews the diff and verify answers
→ open PR / fork from any round with fresh hints / discard.

Questions auto-archive when a PR they produced merges.

## State

Everything persists under `~/.panda/studio/` (`STUDIO_DATA_DIR` to override):
questions + full traces, fix pipelines (worktrees under `fixes/<id>/worktree`),
and local case-export drafts. Server restarts never lose data; interrupted runs
get retry buttons and interrupted fix pipelines a resume action.

Knobs: `STUDIO_PORT` (2499), `STUDIO_RUN_TIMEOUT` (240s), `STUDIO_MAX_CONCURRENT`
(5 question runs), `STUDIO_MAX_FIXES` (10 concurrent pipelines), `STUDIO_CODEX_MODEL`
(gpt-5.5), `STUDIO_JUDGE_MODEL` (openai/gpt-5.4-mini via OpenRouter),
`STUDIO_PR_POLL_SECS` (180), `STUDIO_SANDBOX=0` (host-mode agents, debugging only).
