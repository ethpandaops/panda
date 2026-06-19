"""promptfoo custom grading provider that runs the llm-rubric judge through the Codex
Responses API directly.

promptfoo's ``llm-rubric`` assert renders a grading prompt (the rubric + the subject's
output) and sends it to the test's grading provider, then parses the provider's ``output``
for a ``{"pass": bool, "score": float, "reason": str}`` verdict. The default grader is a
direct OpenAI-compatible API call (``openai:chat``), which needs an API key.

This provider instead calls the Codex Responses API directly, authenticating from the
ChatGPT OAuth credential in ``~/.codex/auth.json`` — the same Codex subscription the
subject runs on. That credential is codex-scoped and rejected by a direct
``api.openai.com`` call (``403 Missing scopes: api.model.read``), but the Codex endpoint
accepts it, so a judge can grade on the user's Codex subscription with NO OpenAI API key
and NO OpenRouter detour.

The transport (auth, streaming SSE accumulation, watchdog/retry) is a trimmed port of
ethpandaops/ai-evals ``providers/openai-codex.py``.

Wired in via ``config.settings.grader_for``: a judge spec like ``gpt-5.5`` (or an explicit
``codex:gpt-5.5``) resolves to this ``file://`` provider with the Codex model and reasoning
effort in its config.
"""

from __future__ import annotations

import json
import os
import threading
import time
from pathlib import Path

import requests

API_URL = "https://chatgpt.com/backend-api/codex/responses"
AUTH_JSON_PATH = Path.home() / ".codex" / "auth.json"
SOCKET_TIMEOUT_S = int(os.environ.get("CODEX_SOCKET_TIMEOUT_S", 300))
CONTENT_STALL_TIMEOUT_S = int(os.environ.get("CODEX_CONTENT_STALL_TIMEOUT_S", 180))
WATCHDOG_CHECK_INTERVAL_S = 5
PREMATURE_STREAM_MAX_RETRIES = 2
PREMATURE_STREAM_RETRY_BACKOFF_S = 2.0


def _load_auth() -> tuple[str | None, str | None, str | None]:
    """Read access_token and account_id from ~/.codex/auth.json. No API key."""
    if not AUTH_JSON_PATH.is_file():
        return (
            None,
            None,
            f"Codex auth file not found at {AUTH_JSON_PATH}. Run `codex login` first.",
        )
    try:
        data = json.loads(AUTH_JSON_PATH.read_text())
    except (OSError, json.JSONDecodeError) as exc:
        return None, None, f"Failed to parse {AUTH_JSON_PATH}: {exc}"
    tokens = data.get("tokens", {}) or {}
    access_token = tokens.get("access_token")
    account_id = tokens.get("account_id")
    if not access_token:
        return None, None, "No tokens.access_token in auth.json"
    if not account_id:
        return None, None, "No tokens.account_id in auth.json"
    return access_token, account_id, None


def _is_retryable_stream_error(exc: Exception) -> bool:
    if isinstance(exc, requests.exceptions.ChunkedEncodingError):
        return True
    text = str(exc).lower()
    return "response ended prematurely" in text or "incomplete read" in text


def _do_request(
    model: str,
    instructions: str,
    input_messages: list,
    reasoning_effort: str | None,
    access_token: str,
    account_id: str,
) -> tuple[str, str | None, bool]:
    """One streaming Codex request. Returns (text, error, retryable)."""
    body = {
        "model": model,
        "instructions": instructions,
        "input": input_messages,
        "store": False,
        "stream": True,
    }
    if reasoning_effort:
        body["reasoning"] = {"effort": reasoning_effort}
    headers = {
        "Content-Type": "application/json",
        "Authorization": f"Bearer {access_token}",
        "ChatGPT-Account-ID": account_id,
    }

    text_parts: list[str] = []
    completed = False
    wd_lock = threading.Lock()
    wd_last = [time.monotonic()]
    wd_stopped = [False]
    wd_reason: list[str | None] = [None]

    try:
        resp = requests.post(
            API_URL, headers=headers, json=body, stream=True, timeout=SOCKET_TIMEOUT_S
        )
        if resp.status_code >= 400:
            return "", f"Codex API error: {resp.status_code} {resp.text[:500]}", False

        def _watchdog() -> None:
            while True:
                time.sleep(WATCHDOG_CHECK_INTERVAL_S)
                with wd_lock:
                    if wd_stopped[0]:
                        return
                    idle = time.monotonic() - wd_last[0]
                if idle > CONTENT_STALL_TIMEOUT_S:
                    with wd_lock:
                        wd_reason[0] = f"Content stall: no events for {idle:.0f}s"
                    try:
                        resp.close()
                    except Exception:  # noqa: BLE001 - best-effort abort
                        pass
                    return

        wd_thread = threading.Thread(target=_watchdog, daemon=True)
        wd_thread.start()
        try:
            for raw_line in resp.iter_lines():
                if not raw_line:
                    continue
                line = raw_line.decode("utf-8") if isinstance(raw_line, bytes) else raw_line
                if not line.startswith("data: "):
                    continue
                try:
                    data = json.loads(line[len("data: ") :])
                except json.JSONDecodeError:
                    continue
                with wd_lock:
                    wd_last[0] = time.monotonic()
                event_type = data.get("type", "")
                if event_type == "response.output_text.delta":
                    delta = data.get("delta", "")
                    if delta:
                        text_parts.append(delta)
                elif event_type == "response.completed":
                    completed = True
                    break
        finally:
            with wd_lock:
                wd_stopped[0] = True
    except (requests.exceptions.RequestException, Exception) as exc:  # noqa: BLE001
        with wd_lock:
            reason = wd_reason[0]
        if reason:
            return "".join(text_parts), reason, False
        if _is_retryable_stream_error(exc):
            return "".join(text_parts), f"Codex stream error: {exc}", True
        return "".join(text_parts), f"Codex request failed: {exc}", False

    output_text = "".join(text_parts)
    if not completed:
        return output_text, "Stream ended before response.completed", True
    return output_text, None, False


def _codex_complete(
    model: str, instructions: str, input_messages: list, reasoning_effort: str | None
) -> str:
    access_token, account_id, err = _load_auth()
    if err:
        raise RuntimeError(err)

    attempt = 0
    while True:
        text, error, retryable = _do_request(
            model, instructions, input_messages, reasoning_effort, access_token, account_id
        )
        if error and retryable and attempt < PREMATURE_STREAM_MAX_RETRIES:
            attempt += 1
            time.sleep(PREMATURE_STREAM_RETRY_BACKOFF_S)
            continue
        if error:
            raise RuntimeError(error)
        return text


def _split_messages(prompt: str) -> tuple[str, list]:
    """promptfoo renders the rubric as a JSON chat array (system + user). Split the system
    message into Responses-API ``instructions`` and keep the rest as ``input``. A bare
    string prompt becomes a single user message."""
    try:
        messages = json.loads(prompt)
    except (json.JSONDecodeError, TypeError):
        return "You are a helpful assistant.", [{"role": "user", "content": prompt}]
    if not isinstance(messages, list):
        return "You are a helpful assistant.", [{"role": "user", "content": str(prompt)}]
    instructions = None
    input_messages = []
    for msg in messages:
        if isinstance(msg, dict) and msg.get("role") == "system":
            instructions = msg.get("content", "")
        else:
            input_messages.append(msg)
    return instructions or "You are a helpful assistant.", input_messages


def call_api(prompt, options, context):
    cfg = (options or {}).get("config", {}) or {}
    model = cfg.get("model") or "gpt-5.5"
    reasoning_effort = cfg.get("reasoning_effort")
    instructions, input_messages = _split_messages(prompt)
    try:
        output = _codex_complete(model, instructions, input_messages, reasoning_effort)
    except Exception as exc:  # noqa: BLE001 - surface as a provider error promptfoo reports
        return {"error": f"codex judge ({model}) failed: {exc}"}
    # llm-rubric parses the JSON verdict out of `output` itself; just hand back the text.
    return {"output": output}
