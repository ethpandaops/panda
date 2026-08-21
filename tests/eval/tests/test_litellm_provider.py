"""Both ends of the eval can ride a LiteLLM proxy.

The zen gateway occasionally drops a model out from under the pinned subject
(deepseek-v4-flash went opt-in mid-2026 and every smoke case came back empty), so
subject and judge each need a second transport that is not zen.
"""

import pytest

from agent.opencode_agent import OpenCodeAgent
from config.settings import EvalSettings, grader_for


def _agent(monkeypatch, model: str, **env) -> OpenCodeAgent:
    for k, v in env.items():
        monkeypatch.setenv(k, v)
    return OpenCodeAgent(EvalSettings(model=model))


def test_judge_grades_through_the_proxy(monkeypatch):
    monkeypatch.setenv("LITELLM_PROXY_URL", "https://ai.example.com/")
    spec = grader_for("litellm/minimax-m2.7")

    assert spec["id"] == "openai:chat:minimax-m2.7"
    # trailing slash trimmed, /v1 appended exactly once
    assert spec["config"]["apiBaseUrl"] == "https://ai.example.com/v1"
    assert spec["config"]["apiKeyEnvar"] == "LITELLM_PROXY_API_KEY"


def test_judge_without_a_url_says_which_var(monkeypatch):
    monkeypatch.delenv("LITELLM_PROXY_URL", raising=False)
    with pytest.raises(ValueError, match="LITELLM_PROXY_URL"):
        grader_for("litellm/minimax-m2.7")


@pytest.mark.parametrize("model", ["qwen3.7-plus", "codex/gpt-5.4"])
def test_other_transports_are_untouched(monkeypatch, model):
    monkeypatch.setenv("LITELLM_PROXY_URL", "https://ai.example.com")
    spec = grader_for(model)
    assert "ai.example.com" not in repr(spec)


def test_subject_declares_the_provider(monkeypatch):
    agent = _agent(
        monkeypatch,
        "litellm/minimax-m2.7",
        LITELLM_PROXY_URL="https://ai.example.com",
        LITELLM_PROXY_API_KEY="k",
    )
    cfg = agent._opencode_config()

    assert cfg["model"] == "litellm/minimax-m2.7"
    provider = cfg["provider"]["litellm"]
    assert provider["npm"] == "@ai-sdk/openai-compatible"
    assert provider["options"]["baseURL"] == "https://ai.example.com/v1"
    assert provider["options"]["apiKey"] == "k"
    # opencode cannot resolve litellm/<model> unless the model is declared
    assert "minimax-m2.7" in provider["models"]


def test_subject_without_a_url_says_which_var(monkeypatch):
    agent = _agent(monkeypatch, "litellm/minimax-m2.7", LITELLM_PROXY_API_KEY="k")
    monkeypatch.delenv("LITELLM_PROXY_URL", raising=False)
    with pytest.raises(ValueError, match="LITELLM_PROXY_URL"):
        agent._opencode_config()


def test_zen_subject_declares_no_provider(monkeypatch):
    monkeypatch.setenv("LITELLM_PROXY_URL", "https://ai.example.com")
    agent = _agent(monkeypatch, "opencode-go/mimo-v2.5")
    assert "provider" not in agent._opencode_config()
