"""promptfoo custom grading provider that runs the llm-rubric judge through opencode.

promptfoo's ``llm-rubric`` assert renders a grading prompt (the rubric + the subject's
output) and sends it to the test's grading provider, then parses the provider's ``output``
for a ``{"pass": bool, "score": float, "reason": str}`` verdict. The default grader is a
direct OpenAI-compatible API call (``openai:chat``), which needs an API key.

This provider instead routes that same grading prompt through opencode's ``session.chat``
against the configured provider/model (e.g. ``openai/gpt-5.5``). For the ``openai``
provider that authenticates from opencode's ``auth.json`` Codex/ChatGPT OAuth credential —
the exact path the subject already uses — so a judge can grade on the user's Codex
subscription with NO OpenAI API key and NO OpenRouter detour.

Wired in via ``config.settings.grader_for``: a judge spec like ``openai/gpt-5.x`` (or
``opencode:openai/gpt-5.x``) resolves to this ``file://`` provider with the provider/model
in its config.
"""

import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))  # tests/eval on path

from agent.opencode_judge import grade_async


def call_api(prompt, options, context):
    cfg = (options or {}).get("config", {}) or {}
    provider_id = cfg.get("provider_id") or "openai"
    model_id = cfg.get("model_id") or "gpt-5.5"
    timeout = float(cfg.get("timeout", 120.0))
    try:
        output = grade_async(provider_id, model_id, prompt, timeout=timeout)
    except Exception as exc:  # noqa: BLE001 - surface as a provider error promptfoo reports
        return {"error": f"opencode judge ({provider_id}/{model_id}) failed: {exc}"}
    # llm-rubric parses the JSON verdict out of `output` itself; just hand back the text.
    return {"output": output}
