"""panda case studio — one-off web UI for minting eval test cases.

Submit a question; N independent opencode agents resolve it against the local
panda; review the completed runs (full tool-call traces), tick which answers
are correct, and export a draft case YAML + approved traces for a follow-up
agent to fold into tests/eval/cases/.

Run:
    cd panda-case-studio
    uv run --project ../panda/tests/eval --with fastapi --with 'uvicorn[standard]' python server.py
"""

from __future__ import annotations

import asyncio
import json
import os
import re
import shutil
import subprocess
import sys
import time
import uuid
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

STUDIO_DIR = Path(__file__).resolve().parent
EVAL_DIR = STUDIO_DIR.parent
sys.path.insert(0, str(EVAL_DIR))

from fastapi import FastAPI, HTTPException  # noqa: E402
from fastapi.responses import FileResponse, JSONResponse  # noqa: E402
from fastapi.staticfiles import StaticFiles  # noqa: E402

from agent.opencode_agent import OpenCodeAgent  # noqa: E402
from config.settings import EvalSettings  # noqa: E402
from scripts._panda_env import (  # noqa: E402
    OPENCODE_IMAGE,
    cross_build_panda_linux,
    ensure_opencode_image,
)

from studio import fixes as fixmod  # noqa: E402
from studio import case_pr as casepr  # noqa: E402

DATA_DIR = Path(os.environ.get("STUDIO_DATA_DIR", "~/.panda/studio")).expanduser()
QUESTIONS_DIR = DATA_DIR / "questions"
APPROVED_DIR = DATA_DIR / "approved"
QUESTIONS_DIR.mkdir(parents=True, exist_ok=True)
APPROVED_DIR.mkdir(parents=True, exist_ok=True)

RUN_TIMEOUT = float(os.environ.get("STUDIO_RUN_TIMEOUT", "240"))
MAX_CONCURRENT_RUNS = int(os.environ.get("STUDIO_MAX_CONCURRENT", "5"))
LIVE_POLL_SECS = 1.5
# Containerized agents by default: opencode (and its shell) run inside the
# panda-opencode-eval image with NO host filesystem — only a cross-built linux
# panda binary + a config pointing at host.docker.internal. Same isolation the
# harden loop / eval CI use. STUDIO_SANDBOX=0 reverts to host mode.
SANDBOX_DEFAULT = os.environ.get("STUDIO_SANDBOX", "1").lower() not in ("0", "false")
SANDBOX_BIN_DIR = DATA_DIR / "sandbox-bin"
SANDBOX_SERVER_URL = os.environ.get(
    "STUDIO_SANDBOX_SERVER_URL", "http://host.docker.internal:2480"
)

DEFAULT_MODELS = [
    "opencode-go/deepseek-v4-flash",
    "opencode-go/mimo-v2.5",
]
KNOWN_MODELS = [
    "opencode-go/deepseek-v4-flash",
    "opencode-go/mimo-v2.5",
    "opencode-go/qwen3.7-plus",
    "opencode-go/deepseek-v4-pro",
    "opencode-go/minimax-m3",
]


def _seed_opencode_key() -> None:
    """The harness requires OPENCODE_GO_API_KEY in the env for the opencode-go
    provider; lift it from the opencode auth store when not already exported."""
    if os.environ.get("OPENCODE_GO_API_KEY") or os.environ.get("OPENCODE_API_KEY"):
        return
    base = Path(os.environ.get("XDG_DATA_HOME") or (Path.home() / ".local" / "share"))
    auth = base / "opencode" / "auth.json"
    try:
        entry = json.loads(auth.read_text()).get("opencode-go") or {}
        if entry.get("type") == "api" and entry.get("key"):
            os.environ["OPENCODE_GO_API_KEY"] = entry["key"]
    except Exception:
        pass


def now_iso() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="seconds")


def atomic_write(path: Path, text: str) -> None:
    tmp = path.with_suffix(path.suffix + ".tmp")
    tmp.write_text(text)
    tmp.replace(path)


# ---------------------------------------------------------------------------
# state
# ---------------------------------------------------------------------------

QUESTIONS: dict[str, dict[str, Any]] = {}
AGENTS: dict[tuple[str, str, bool], OpenCodeAgent] = {}
RUN_TASKS: dict[tuple[str, int], asyncio.Task] = {}
SEM = asyncio.Semaphore(MAX_CONCURRENT_RUNS)
AGENT_LOCK = asyncio.Lock()

TERMINAL = ("complete", "error", "interrupted")


def save_question(q: dict[str, Any]) -> None:
    atomic_write(QUESTIONS_DIR / f"{q['id']}.json", json.dumps(q, indent=2, default=str))


def load_questions() -> None:
    for f in sorted(QUESTIONS_DIR.glob("q_*.json")):
        try:
            q = json.loads(f.read_text())
        except Exception:
            continue
        # Anything mid-flight when the previous server died is dead now.
        for run in q.get("runs", []):
            if run.get("status") in ("queued", "running"):
                run["status"] = "interrupted"
                run["error"] = "server restarted mid-run"
        QUESTIONS[q["id"]] = q
        save_question(q)


def question_status(q: dict[str, Any]) -> str:
    statuses = [r["status"] for r in q["runs"]]
    if any(s in ("queued", "running") for s in statuses):
        return "running"
    if q.get("archived_at"):
        return "archived"
    if q.get("review", {}).get("exported_at"):
        return "exported"
    if q.get("review", {}).get("reviewed"):
        return "reviewed"
    return "needs_review"


def latest_fix(qid: str) -> dict[str, Any] | None:
    fs = [f for f in fixmod.FIXES.values() if f["question_id"] == qid]
    if not fs:
        return None
    return fixmod.fix_summary(max(fs, key=lambda f: f["created_at"]))


def question_summary(q: dict[str, Any]) -> dict[str, Any]:
    return {
        "fix": latest_fix(q["id"]),
        "id": q["id"],
        "question": q["question"],
        "network": q["network"],
        "route": q["route"],
        "sandbox": q.get("sandbox", False),
        "expectation": q.get("expectation", ""),
        "created_at": q["created_at"],
        "status": question_status(q),
        "runs": [
            {
                "idx": r["idx"],
                "model": r["model"],
                "status": r["status"],
                "verdict": r.get("verdict"),
                "n_tools": len(r.get("tool_calls", [])),
                "duration_ms": r.get("duration_ms"),
            }
            for r in q["runs"]
        ],
        "review": q.get("review", {}),
    }


# ---------------------------------------------------------------------------
# agent plumbing
# ---------------------------------------------------------------------------


_SANDBOX_READY = False


def _prepare_sandbox() -> None:
    """Ensure the opencode sandbox image + a linux panda binary (built from the
    local panda repo) and point the agent env at them. The binary is copied to a
    studio-owned dir so a fix-pipeline cross-build (from a worktree) can never
    swap it out from under in-flight question runs."""
    global _SANDBOX_READY
    if _SANDBOX_READY:
        return
    ensure_opencode_image()
    prior = os.environ.get("OPENCODE_SANDBOX_PANDA_BIN")
    built = cross_build_panda_linux(str(fixmod.PANDA_REPO))
    if prior is not None:
        os.environ["OPENCODE_SANDBOX_PANDA_BIN"] = prior
    SANDBOX_BIN_DIR.mkdir(parents=True, exist_ok=True)
    shutil.copy2(built, SANDBOX_BIN_DIR / "panda")
    os.environ["OPENCODE_SANDBOX_PANDA_BIN"] = str(SANDBOX_BIN_DIR / "panda")
    os.environ["OPENCODE_SANDBOX_SERVER_URL"] = SANDBOX_SERVER_URL
    os.environ["OPENCODE_SANDBOX_IMAGE"] = OPENCODE_IMAGE
    _SANDBOX_READY = True


async def get_agent(model: str, route: str, sandbox: bool) -> OpenCodeAgent:
    key = (model, route, sandbox)
    async with AGENT_LOCK:
        if sandbox:
            await asyncio.to_thread(_prepare_sandbox)
        if key not in AGENTS:
            settings = EvalSettings(
                model=model,
                opencode_route=route,
                opencode_timeout=RUN_TIMEOUT,
                langfuse_enabled=False,
                opencode_sandbox=sandbox,
                mcp_url=SANDBOX_SERVER_URL if sandbox else "http://localhost:2480",
            )
            AGENTS[key] = OpenCodeAgent(settings)
    agent = AGENTS[key]
    await agent._ensure_server()  # noqa: SLF001 - one-off tool, harness-internal API
    return agent


def _as_dict(obj: Any) -> dict[str, Any]:
    if hasattr(obj, "model_dump"):
        return obj.model_dump(warnings=False)
    return obj if isinstance(obj, dict) else {}


def _norm_tool(name: str | None) -> str:
    return OpenCodeAgent._norm_tool(name)  # noqa: SLF001


def _stringify(v: Any) -> str:
    if v is None:
        return ""
    if isinstance(v, str):
        return v
    try:
        return json.dumps(v, indent=2, default=str)
    except Exception:
        return str(v)


def _finalize_trace(run: dict[str, Any]) -> None:
    """A dead run can't have in-flight tool calls: anything still pending/running
    when it died is marked interrupted so the UI doesn't show it as LIVE."""
    for tc in run.get("tool_calls", []):
        if tc.get("status") in ("pending", "running"):
            tc["status"] = "interrupted"


def parse_live_messages(items: list[Any]) -> tuple[list[dict[str, Any]], str]:
    """Map a fresh session's messages into tool-call dicts + latest answer text.

    Mirrors OpenCodeAgent.execute()'s transcript walk, but keeps in-flight tool
    calls (status pending/running) so the UI can show live progress."""
    tool_calls: list[dict[str, Any]] = []
    final_text = ""
    for item in items:
        d = _as_dict(item)
        info = d.get("info", {}) or {}
        if info.get("role") != "assistant":
            continue
        for p in d.get("parts", []) or []:
            if not isinstance(p, dict):
                continue
            ty = p.get("type")
            if ty == "tool":
                st = p.get("state") or {}
                status = st.get("status") or "pending"
                tool_calls.append(
                    {
                        "name": _norm_tool(p.get("tool")),
                        "input": _stringify(st.get("input")),
                        "output": _stringify(st.get("output")),
                        "status": "error" if status == "error" else status,
                        "duration_ms": OpenCodeAgent._tool_duration_ms(st),  # noqa: SLF001
                    }
                )
            elif ty == "text" and p.get("text"):
                final_text = p["text"]
    return tool_calls, final_text


async def live_poll(client: Any, sid: str, q: dict[str, Any], run: dict[str, Any]) -> None:
    """Stream partial progress into the run record while execute() is in flight."""
    while True:
        await asyncio.sleep(LIVE_POLL_SECS)
        try:
            msgs = await client.session.messages(id=sid)
            tool_calls, text = parse_live_messages(msgs)
            run["tool_calls"] = tool_calls
            if text:
                run["answer"] = text
            run["elapsed_ms"] = int(time.time() * 1000) - run["_started_ms"]
            save_question(q)
        except asyncio.CancelledError:
            raise
        except Exception:
            pass  # transient poll failures never kill the run


# The judge rides the same opencode zen gateway (OpenAI-compatible) and key as the
# agent subjects — qwen3.7-plus per the eval harness's #195 benching; no OpenRouter.
JUDGE_MODEL = os.environ.get("STUDIO_JUDGE_MODEL", "qwen3.7-plus")
OPENCODE_ZEN_BASE_URL = "https://opencode.ai/zen/go/v1"


def _judge_key() -> str:
    return os.environ.get("OPENCODE_GO_API_KEY") or os.environ.get("OPENCODE_API_KEY") or ""


def _extract_json(text: str) -> dict[str, Any]:
    """Lenient JSON pull: zen models sometimes wrap the object in prose/fences."""
    try:
        return json.loads(text)
    except ValueError:
        start, end = text.find("{"), text.rfind("}")
        if start < 0 or end <= start:
            raise
        return json.loads(text[start : end + 1])


async def judge_json(prompt: str, timeout: float = 90) -> dict[str, Any]:
    """One JSON-answering judge call via the zen gateway."""
    import httpx

    async with httpx.AsyncClient(timeout=timeout) as client:
        resp = await client.post(
            f"{OPENCODE_ZEN_BASE_URL}/chat/completions",
            headers={"Authorization": f"Bearer {_judge_key()}"},
            json={
                "model": JUDGE_MODEL,
                "messages": [{"role": "user", "content": prompt}],
            },
        )
        resp.raise_for_status()
        return _extract_json(resp.json()["choices"][0]["message"]["content"])

_AUTO_REVIEW_PROMPT = """\
You are pre-screening an AI agent's run for a human reviewer. The reviewer gave a rough \
expectation of what a good run looks like — it may describe the answer's content, a path \
the agent should take (a specific table, datasource, or command), or both.

QUESTION the agent was asked:
{question}

REVIEWER'S ROUGH EXPECTATION:
{expectation}

AGENT'S FINAL ANSWER:
{answer}

TOOL CALLS the agent made (harness-captured, trustworthy evidence of the path taken):
{tools}

Does the run satisfy the expectation? When the expectation names a path, verify it in the \
tool calls, not the answer text. Reply with JSON: {{"meets": true|false, "reason": "<one \
short sentence a reviewer can read at a glance>"}}"""


async def auto_review(q: dict[str, Any], run: dict[str, Any]) -> None:
    """First-pass triage of a completed run against the reviewer's expectation:
    an LLM judge suggests a verdict (stored with its reason) and pre-sets the
    run's verdict if the human hasn't set one. Best-effort — failures only mark
    the run as unreviewed."""
    tools = "\n".join(
        f"- {tc['name']}{' [ERROR]' if tc['status'] == 'error' else ''}: "
        f"{' '.join(str(tc['input'])[:300].split())}"
        for tc in run.get("tool_calls", [])[:25]
    ) or "(none)"
    prompt = _AUTO_REVIEW_PROMPT.format(
        question=q["question"],
        expectation=q["expectation"],
        answer=(run.get("answer") or "(no answer)")[:2500],
        tools=tools,
    )
    try:
        verdict_json = await judge_json(prompt, timeout=60)
        verdict = "correct" if verdict_json.get("meets") else "incorrect"
        run["auto_review"] = {
            "verdict": verdict,
            "reason": str(verdict_json.get("reason", ""))[:300],
        }
        if run.get("verdict") is None:
            run["verdict"] = verdict
            run["verdict_auto"] = True
    except Exception as exc:  # noqa: BLE001 - triage is best-effort
        run["auto_review"] = {"verdict": None, "reason": f"auto-review failed: {exc}"[:200]}
    save_question(q)


async def execute_run(q: dict[str, Any], run: dict[str, Any]) -> None:
    async with SEM:
        run["status"] = "running"
        run["started_at"] = now_iso()
        run["_started_ms"] = int(time.time() * 1000)
        save_question(q)
        poller: asyncio.Task[None] | None = None
        try:
            agent = await get_agent(run["model"], q["route"], q.get("sandbox", False))
            client = agent._client  # noqa: SLF001
            sess = await client.session.create()
            sid = sess.id
            run["session_id"] = sid
            poller = asyncio.create_task(live_poll(client, sid, q, run))
            result = await agent.execute(q["question"], session_id=sid, test_id=q["id"])

            if result.is_error and not result.tool_calls:
                # Timeout/abort paths return an empty transcript — harvest the
                # session's real message state so the steps still render.
                try:
                    msgs = await client.session.messages(id=sid)
                    tool_calls, text = parse_live_messages(msgs)
                    if tool_calls:
                        run["tool_calls"] = tool_calls
                    if text and not run.get("answer"):
                        run["answer"] = text
                except Exception:
                    pass  # keep whatever the live poller captured
                _finalize_trace(run)
            else:
                run["tool_calls"] = [
                    {
                        "name": tc.name,
                        "input": _stringify(tc.input),
                        "output": _stringify(tc.result),
                        "status": "error" if tc.is_error else "completed",
                        "duration_ms": tc.duration_ms,
                    }
                    for tc in result.tool_calls
                ]
                run["answer"] = result.output
            run["tokens"] = {"input": result.input_tokens, "output": result.output_tokens}
            run["cost_usd"] = result.total_cost_usd
            run["duration_ms"] = result.duration_ms
            if result.is_error:
                run["status"] = "error"
                run["error"] = result.error_message
            else:
                run["status"] = "complete"
                if q.get("expectation"):
                    await auto_review(q, run)
        except asyncio.CancelledError:
            # Human pulled the plug: keep the partial trace, flag the run as a
            # failure so it counts as evidence for the fix pipeline.
            run["status"] = "cancelled"
            run["error"] = "cancelled by reviewer (flagged as failure)"
            run["verdict"] = "incorrect"
            _finalize_trace(run)
            raise
        except Exception as exc:  # noqa: BLE001 - surface anything to the UI
            run["status"] = "error"
            run["error"] = f"{type(exc).__name__}: {exc}"
        finally:
            if poller:
                poller.cancel()
            run["finished_at"] = now_iso()
            run.pop("_started_ms", None)
            run.pop("elapsed_ms", None)
            save_question(q)
            RUN_TASKS.pop((q["id"], run["idx"]), None)


# ---------------------------------------------------------------------------
# api
# ---------------------------------------------------------------------------

app = FastAPI(title="panda case studio")


@app.get("/api/meta")
async def meta() -> dict[str, Any]:
    return {
        "models": KNOWN_MODELS,
        "default_models": DEFAULT_MODELS,
        "default_runs": 5,
        "panda_url": os.environ.get("MCP_EVAL_MCP_URL", "http://localhost:2480"),
        "run_timeout": RUN_TIMEOUT,
        "sandbox_default": SANDBOX_DEFAULT,
        "cases_files": await asyncio.to_thread(casepr.list_cases_files),
    }


@app.get("/api/questions")
async def list_questions() -> list[dict[str, Any]]:
    qs = sorted(QUESTIONS.values(), key=lambda q: q["created_at"], reverse=True)
    return [question_summary(q) for q in qs]


@app.get("/api/questions/{qid}")
async def get_question(qid: str) -> dict[str, Any]:
    q = QUESTIONS.get(qid)
    if not q:
        raise HTTPException(404, "no such question")
    out = {k: v for k, v in q.items()}
    out["status"] = question_status(q)
    out["fixes"] = [
        fixmod.fix_detail(f)
        for f in sorted(
            (f for f in fixmod.FIXES.values() if f["question_id"] == qid),
            key=lambda f: f["created_at"],
        )
    ]
    return out


@app.post("/api/questions")
async def submit_question(body: dict[str, Any]) -> dict[str, Any]:
    question = (body.get("question") or "").strip()
    if not question:
        raise HTTPException(400, "question is required")
    models = [m for m in (body.get("models") or DEFAULT_MODELS) if m]
    n_runs = max(1, min(int(body.get("runs") or 5), 10))
    route = body.get("route") or "cli"
    if route not in ("cli", "mcp"):
        raise HTTPException(400, "route must be cli or mcp")

    qid = "q_" + uuid.uuid4().hex[:10]
    q: dict[str, Any] = {
        "id": qid,
        "question": question,
        "network": (body.get("network") or "mainnet").strip() or "mainnet",
        "route": route,
        "sandbox": True,
        "expectation": (body.get("expectation") or "").strip(),
        "created_at": now_iso(),
        "runs": [
            {
                "idx": i,
                "model": models[i % len(models)],
                "status": "queued",
                "answer": "",
                "error": None,
                "tool_calls": [],
                "verdict": None,
                "tokens": None,
                "cost_usd": None,
                "duration_ms": None,
            }
            for i in range(n_runs)
        ],
        "review": {"reviewed": False, "case_id": None, "rubric": "", "exported_at": None},
    }
    QUESTIONS[qid] = q
    save_question(q)
    for run in q["runs"]:
        RUN_TASKS[(qid, run["idx"])] = asyncio.create_task(execute_run(q, run))
    return question_summary(q)


RETRYABLE = ("interrupted", "error", "cancelled")


def _reset_run(run: dict[str, Any]) -> None:
    run.update(
        status="queued", answer="", error=None, tool_calls=[], verdict=None,
        tokens=None, cost_usd=None, duration_ms=None, session_id=None,
    )


@app.post("/api/questions/{qid}/runs/{idx}/retry")
async def retry_run(qid: str, idx: int) -> dict[str, Any]:
    q = QUESTIONS.get(qid)
    if not q or idx >= len(q["runs"]):
        raise HTTPException(404, "no such run")
    run = q["runs"][idx]
    if run["status"] not in RETRYABLE:
        raise HTTPException(400, f"run is {run['status']}; retry applies to {'/'.join(RETRYABLE)}")
    _reset_run(run)
    save_question(q)
    RUN_TASKS[(qid, idx)] = asyncio.create_task(execute_run(q, run))
    return {"ok": True}


@app.post("/api/questions/{qid}/retry-failed")
async def retry_failed(qid: str) -> dict[str, Any]:
    q = QUESTIONS.get(qid)
    if not q:
        raise HTTPException(404, "no such question")
    retried = []
    for run in q["runs"]:
        if run["status"] in RETRYABLE:
            _reset_run(run)
            retried.append(run["idx"])
    if not retried:
        raise HTTPException(400, "no failed runs to retry")
    save_question(q)
    for i in retried:
        RUN_TASKS[(qid, i)] = asyncio.create_task(execute_run(q, q["runs"][i]))
    return {"ok": True, "retried": retried}


@app.post("/api/questions/{qid}/runs/{idx}/cancel")
async def cancel_run(qid: str, idx: int) -> dict[str, Any]:
    q = QUESTIONS.get(qid)
    if not q or idx >= len(q["runs"]):
        raise HTTPException(404, "no such run")
    run = q["runs"][idx]
    if run["status"] not in ("queued", "running"):
        raise HTTPException(400, f"run is {run['status']}, nothing to cancel")
    task = RUN_TASKS.get((qid, idx))
    if task and not task.done():
        task.cancel()
    else:
        run["status"] = "cancelled"
        run["error"] = "cancelled by reviewer (flagged as failure)"
        run["verdict"] = "incorrect"
        save_question(q)
    return {"ok": True}


@app.delete("/api/questions/{qid}")
async def delete_question(qid: str) -> dict[str, Any]:
    q = QUESTIONS.pop(qid, None)
    if not q:
        raise HTTPException(404, "no such question")
    (QUESTIONS_DIR / f"{qid}.json").unlink(missing_ok=True)
    return {"ok": True}


@app.post("/api/questions/{qid}/runs/{idx}/verdict")
async def set_verdict(qid: str, idx: int, body: dict[str, Any]) -> dict[str, Any]:
    q = QUESTIONS.get(qid)
    if not q or idx >= len(q["runs"]):
        raise HTTPException(404, "no such run")
    verdict = body.get("verdict")
    if verdict not in ("correct", "incorrect", None):
        raise HTTPException(400, "verdict must be correct, incorrect, or null")
    q["runs"][idx]["verdict"] = verdict
    q["runs"][idx]["verdict_auto"] = False
    save_question(q)
    return {"ok": True, "verdict": verdict}


def slugify(text: str) -> str:
    s = re.sub(r"[^a-z0-9]+", "_", text.lower()).strip("_")
    return s[:48] or "case"


def draft_rubric(q: dict[str, Any]) -> str:
    approved = [r for r in q["runs"] if r.get("verdict") == "correct" and r.get("answer")]
    if approved:
        header = "TODO: refine into a real rubric. Approved answers for reference:"
        pool = approved
    else:
        # Nothing ticked yet — seed from every completed answer so the reviewer
        # has raw material either way; ticking ✓ re-drafts from approved only.
        pool = [r for r in q["runs"] if r.get("status") == "complete" and r.get("answer")]
        if not pool:
            return ""
        header = (
            "TODO: refine into a real rubric. No runs ticked correct yet — "
            "ALL completed answers below for reference (tick ✓ to narrow):"
        )
    lines = [header, ""]
    for r in pool:
        snippet = " ".join(r["answer"].split())[:400]
        lines.append(f"- [{r['model']}] {snippet}")
    return "\n".join(lines)


_DRAFT_PROMPT = """\
You are writing an eval test case for `panda`, an Ethereum analytics CLI/MCP harness. \
A human asked AI agents the question below and reviewed their answers; you draft the \
case from the approved evidence.

THE QUESTION (network={network}):
{question}

{expectation_line}APPROVED RUNS (answer + tool path, harness-captured):
{approved}

{rejected_line}HOUSE RUBRIC STYLE (these are real examples — match their shape):
- "Reports a p50 (median) block arrival time for recent mainnet blocks, derived from a \
real ClickHouse query against block-timing data. An answer with no number, a \
hallucinated figure, or one wildly outside that range fails."
- The rubric describes WHAT a correct answer looks like, never HOW to get it (no \
expected-tools/tables assertions — the judge sees the captured tool calls and verifies \
"from a real query" claims itself).
- Be concrete: name the expected magnitude/range/identity from the approved answers so \
a hallucination fails, but phrase it so the case survives data drift (say "roughly", \
"on the order of", or name the stable fact rather than a volatile number when possible).
- One or two sentences of failure conditions at the end.

Existing cases files (pick the best-fitting one): {cases_files}

Reply with JSON:
{{"case_id": "<snake_case, descriptive, <50 chars>",
  "description": "<one line, lowercase, what the case checks>",
  "tags": ["<2-4 tags: datasource + topic, e.g. clickhouse, xatu, peers>"],
  "cases_file": "<one of the existing files>",
  "rubric": "<the llm-rubric value, house style>"}}"""


@app.post("/api/questions/{qid}/auto-draft")
async def auto_draft(qid: str) -> dict[str, Any]:
    q = QUESTIONS.get(qid)
    if not q:
        raise HTTPException(404, "no such question")
    approved = [r for r in q["runs"] if r.get("verdict") == "correct" and r.get("answer")]
    if not approved:
        raise HTTPException(400, "tick at least one run correct first — the rubric drafts from approved evidence")
    rejected = [r for r in q["runs"] if r.get("verdict") == "incorrect" and r.get("answer")]

    def runs_block(runs: list, cap: int = 3) -> str:
        out = []
        for r in runs[:cap]:
            tools = ", ".join(
                f"{tc['name']}({' '.join(str(tc['input'])[:90].split())})"
                for tc in (r.get("tool_calls") or [])[:8]
            )
            out.append(f"- [{r['model']}] {' '.join(r['answer'].split())[:400]}\n  tools: {tools[:600]}")
        return "\n".join(out)

    cases_files = await asyncio.to_thread(casepr.list_cases_files)
    prompt = _DRAFT_PROMPT.format(
        network=q["network"],
        question=q["question"],
        expectation_line=f"REVIEWER'S EXPECTATION: {q['expectation']}\n\n" if q.get("expectation") else "",
        approved=runs_block(approved),
        rejected_line=(
            f"REJECTED RUNS (what failure looked like — the rubric should fail these):\n{runs_block(rejected, 2)}\n\n"
            if rejected else ""
        ),
        cases_files=", ".join(cases_files),
    )
    try:
        draft = await judge_json(prompt)
    except Exception as exc:  # noqa: BLE001
        raise HTTPException(502, f"auto-draft failed: {exc}") from exc
    draft["case_id"] = slugify(str(draft.get("case_id") or slugify(q["question"])))
    if draft.get("cases_file") not in cases_files:
        draft["cases_file"] = ""
    draft["tags"] = [str(t) for t in (draft.get("tags") or [])][:4]
    return draft


@app.get("/api/questions/{qid}/draft")
async def get_draft(qid: str) -> dict[str, Any]:
    q = QUESTIONS.get(qid)
    if not q:
        raise HTTPException(404, "no such question")
    return {
        "case_id": q.get("review", {}).get("case_id") or slugify(q["question"]),
        "rubric": q.get("review", {}).get("rubric") or draft_rubric(q),
    }


@app.post("/api/questions/{qid}/export")
async def export_case(qid: str, body: dict[str, Any]) -> dict[str, Any]:
    q = QUESTIONS.get(qid)
    if not q:
        raise HTTPException(404, "no such question")
    approved = [r for r in q["runs"] if r.get("verdict") == "correct"]
    if not approved:
        raise HTTPException(400, "tick at least one run as correct before exporting")

    case_id = slugify(body.get("case_id") or slugify(q["question"]))
    rubric = (body.get("rubric") or draft_rubric(q)).rstrip()
    tags = [t for t in (body.get("tags") or []) if t]
    description = (body.get("description") or "").strip()

    rubric_block = "\n".join("        " + line for line in rubric.splitlines()) or "        TODO"
    yaml_text = f"""# Draft eval case generated by panda case studio on {now_iso()}.
# Source question {q['id']}; {len(approved)}/{len(q['runs'])} runs approved by human review.
# A follow-up agent should: refine the rubric, pick the right cases file
# (smoke/coverage/multi_step), add tags, and verify with:
#   uv run python -m scripts.eval --cases <file> --question-id {case_id} --no-variations
- id: {case_id}
  description: {json.dumps(description or q['question'])}
  input: {json.dumps(q['question'])}
  network: {q['network']}
  tags: {json.dumps(tags or ['TODO'])}
  assert:
    - type: llm-rubric
      value: >
{rubric_block}
"""
    atomic_write(APPROVED_DIR / f"{case_id}.yaml", yaml_text)

    record = {
        "case_id": case_id,
        "question_id": q["id"],
        "question": q["question"],
        "network": q["network"],
        "route": q["route"],
        "exported_at": now_iso(),
        "rubric_draft": rubric,
        "runs": [
            {
                "model": r["model"],
                "verdict": r.get("verdict"),
                "status": r["status"],
                "answer": r.get("answer"),
                "error": r.get("error"),
                "tokens": r.get("tokens"),
                "duration_ms": r.get("duration_ms"),
                "tool_calls": r.get("tool_calls", []),
            }
            for r in q["runs"]
        ],
    }
    atomic_write(APPROVED_DIR / f"{case_id}.traces.json", json.dumps(record, indent=2))

    q["review"].update(
        {
            "reviewed": True,
            "case_id": case_id,
            "rubric": rubric,
            "exported_at": now_iso(),
        }
    )
    save_question(q)

    out: dict[str, Any] = {
        "ok": True,
        "case_id": case_id,
        "yaml_path": str(APPROVED_DIR / f"{case_id}.yaml"),
        "traces_path": str(APPROVED_DIR / f"{case_id}.traces.json"),
    }
    if body.get("open_pr"):
        cases_file = body.get("cases_file") or ""
        pr = await asyncio.to_thread(
            casepr.open_case_pr,
            q,
            case_id=case_id,
            cases_file=cases_file,
            description=description,
            tags=tags,
            rubric=rubric,
        )
        q["review"]["pr_url"] = pr["pr_url"]
        save_question(q)
        out.update(pr)
    return out


# ---------------------------------------------------------------------------
# model discovery + fix pipeline api
# ---------------------------------------------------------------------------

_MODELS_CACHE: dict[str, Any] = {"at": 0.0, "models": []}


@app.get("/api/models")
async def list_models() -> dict[str, Any]:
    if time.time() - _MODELS_CACHE["at"] > 600 or not _MODELS_CACHE["models"]:
        def _discover() -> list[str]:
            r = subprocess.run(
                ["opencode", "models"], capture_output=True, text=True, timeout=60
            )
            return [l.strip() for l in r.stdout.splitlines() if "/" in l.strip()]

        try:
            models = await asyncio.to_thread(_discover)
            if models:
                _MODELS_CACHE.update(at=time.time(), models=models)
        except Exception:
            pass
    return {"models": _MODELS_CACHE["models"] or KNOWN_MODELS, "defaults": DEFAULT_MODELS}


@app.post("/api/questions/{qid}/fixes")
async def dispatch_fix(qid: str, body: dict[str, Any]) -> dict[str, Any]:
    q = QUESTIONS.get(qid)
    if not q:
        raise HTTPException(404, "no such question")
    if question_status(q) == "running":
        raise HTTPException(400, "wait for the runs to finish first")
    if not (body.get("problem") or "").strip():
        raise HTTPException(400, "describe the problem first")
    # One active fix per QUESTION (the UI shows the latest fix per question);
    # fixes on different questions run concurrently up to STUDIO_MAX_FIXES.
    mine = [
        f for f in fixmod.FIXES.values()
        if f["question_id"] == qid and f["status"] in fixmod.ACTIVE_FIX
    ]
    if mine:
        raise HTTPException(409, f"fix {mine[0]['id']} for this question is still in flight")
    f = fixmod.new_fix(q, body)
    asyncio.get_running_loop().run_in_executor(None, fixmod.run_pipeline, f, q)
    return fixmod.fix_summary(f)


@app.get("/api/fixes/{fid}")
async def get_fix(fid: str) -> dict[str, Any]:
    f = fixmod.FIXES.get(fid)
    if not f:
        raise HTTPException(404, "no such fix")
    return fixmod.fix_detail(f)


@app.get("/api/fixes/{fid}/log")
async def fix_log(fid: str, offset: int = 0) -> dict[str, Any]:
    path = fixmod.FIXES_DIR / f"{fid}.log"
    if not path.exists():
        return {"text": "", "offset": 0}
    data = path.read_bytes()
    return {"text": data[offset:].decode(errors="replace"), "offset": len(data)}


@app.post("/api/fixes/{fid}/pr")
async def fix_pr(fid: str, body: dict[str, Any] | None = None) -> dict[str, Any]:
    f = fixmod.FIXES.get(fid)
    if not f:
        raise HTTPException(404, "no such fix")
    if f["status"] in fixmod.ACTIVE_FIX or f["status"] == "discarded":
        raise HTTPException(400, f"fix is {f['status']}; wait for it to settle")
    round_idx = (body or {}).get("round")
    q = QUESTIONS.get(f["question_id"])
    try:
        url = await asyncio.to_thread(fixmod.open_pr, f, q, round_idx)
    except Exception as exc:  # noqa: BLE001
        raise HTTPException(500, str(exc)) from exc
    return {"ok": True, "pr_url": url}


@app.post("/api/fixes/{fid}/resume")
async def fix_resume(fid: str) -> dict[str, Any]:
    f = fixmod.FIXES.get(fid)
    if not f:
        raise HTTPException(404, "no such fix")
    if f["status"] != "failed":
        raise HTTPException(400, f"fix is {f['status']}; resume only applies to failed fixes")
    q = QUESTIONS.get(f["question_id"])
    asyncio.get_running_loop().run_in_executor(None, fixmod.resume, f, q)
    return {"ok": True}


@app.post("/api/fixes/{fid}/fork")
async def fix_fork(fid: str, body: dict[str, Any]) -> dict[str, Any]:
    f = fixmod.FIXES.get(fid)
    if not f:
        raise HTTPException(404, "no such fix")
    if f["status"] in fixmod.ACTIVE_FIX:
        raise HTTPException(400, f"fix is {f['status']}; wait for the pipeline to settle")
    if f["status"] == "discarded":
        raise HTTPException(400, "fix was discarded")
    hints = (body.get("hints") or "").strip()
    if not hints:
        raise HTTPException(400, "hints required")
    round_idx = int(body.get("round") or f.get("current_round") or 0)
    if not any(r["idx"] == round_idx for r in f.get("rounds", [])):
        raise HTTPException(400, f"no round {round_idx}")
    q = QUESTIONS.get(f["question_id"])
    asyncio.get_running_loop().run_in_executor(None, fixmod.fork, f, q, round_idx, hints)
    return {"ok": True, "from_round": round_idx}


@app.post("/api/fixes/{fid}/discard")
async def fix_discard(fid: str) -> dict[str, Any]:
    f = fixmod.FIXES.get(fid)
    if not f:
        raise HTTPException(404, "no such fix")
    if f["status"] in fixmod.ACTIVE_FIX:
        raise HTTPException(400, "fix is mid-pipeline; wait for it to settle")
    await asyncio.to_thread(fixmod.discard, f)
    return {"ok": True}


# ---------------------------------------------------------------------------
# pr merge watcher — a merged PR archives its question
# ---------------------------------------------------------------------------

PR_POLL_SECS = float(os.environ.get("STUDIO_PR_POLL_SECS", "180"))


def _question_prs(q: dict[str, Any]) -> list[str]:
    urls = []
    if q.get("review", {}).get("pr_url"):
        urls.append(q["review"]["pr_url"])
    urls += [
        f["pr_url"]
        for f in fixmod.FIXES.values()
        if f["question_id"] == q["id"] and f.get("pr_url") and f["status"] == "pr_open"
    ]
    return urls


def _pr_state(url: str) -> str | None:
    try:
        r = subprocess.run(
            ["gh", "pr", "view", url, "--json", "state"],
            capture_output=True, text=True, timeout=30,
        )
        if r.returncode != 0:
            return None
        return json.loads(r.stdout).get("state")
    except Exception:  # noqa: BLE001 - poller is best-effort
        return None


async def watch_pr_merges() -> None:
    while True:
        await asyncio.sleep(PR_POLL_SECS)
        for q in list(QUESTIONS.values()):
            if q.get("archived_at"):
                continue
            urls = _question_prs(q)
            if not urls:
                continue
            states = [await asyncio.to_thread(_pr_state, u) for u in urls]
            merged = [u for u, s in zip(urls, states) if s == "MERGED"]
            if merged:
                q["archived_at"] = now_iso()
                q["archived_reason"] = f"PR merged: {merged[0]}"
                save_question(q)


@app.on_event("startup")
async def _start_watchers() -> None:
    asyncio.create_task(watch_pr_merges())


@app.post("/api/questions/{qid}/archive")
async def archive(qid: str) -> dict[str, Any]:
    q = QUESTIONS.get(qid)
    if not q:
        raise HTTPException(404, "no such question")
    q["archived_at"] = now_iso()
    q["archived_reason"] = "archived manually"
    save_question(q)
    return {"ok": True}


@app.post("/api/questions/{qid}/unarchive")
async def unarchive(qid: str) -> dict[str, Any]:
    q = QUESTIONS.get(qid)
    if not q:
        raise HTTPException(404, "no such question")
    q["archived_at"] = None
    q["archived_reason"] = None
    save_question(q)
    return {"ok": True}


@app.exception_handler(Exception)
async def unhandled(request: Any, exc: Exception) -> JSONResponse:
    return JSONResponse(status_code=500, content={"detail": f"{type(exc).__name__}: {exc}"})


@app.get("/")
async def index() -> FileResponse:
    return FileResponse(STUDIO_DIR / "static" / "index.html")


@app.get("/q/{qid}")
async def index_question(qid: str) -> FileResponse:
    """Path-based deep link — the SPA reads the qid from the URL on boot."""
    return FileResponse(STUDIO_DIR / "static" / "index.html")


app.mount("/static", StaticFiles(directory=STUDIO_DIR / "static"), name="static")


def main() -> None:
    import uvicorn

    _seed_opencode_key()
    load_questions()
    fixmod.load_fixes()
    port = int(os.environ.get("STUDIO_PORT", "2499"))
    print(f"panda case studio → http://127.0.0.1:{port}  (data: {DATA_DIR})")
    uvicorn.run(app, host="127.0.0.1", port=port, log_level="warning")


if __name__ == "__main__":
    main()
