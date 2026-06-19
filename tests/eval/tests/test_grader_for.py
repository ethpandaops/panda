"""grader_for routes judge specs to the right promptfoo grading provider."""

from config.settings import CODEX_JUDGE_REASONING_EFFORT, grader_for


def test_bare_model_uses_opencode_go_gateway():
    g = grader_for("qwen3.7-plus")
    assert g["id"] == "openai:chat:qwen3.7-plus"
    assert g["config"]["apiBaseUrl"].endswith("/zen/go/v1")
    # opencode-go path is keyed off an API key, not a file provider.
    assert "apiKeyEnvar" in g["config"]


def test_codex_model_uses_direct_codex_judge():
    g = grader_for("gpt-5.5")
    assert g["id"].startswith("file://") and g["id"].endswith("promptfoo/judge.py")
    assert g["config"] == {"model": "gpt-5.5", "reasoning_effort": CODEX_JUDGE_REASONING_EFFORT}


def test_other_codex_model_ids_route_to_direct_judge():
    for model in ("gpt-5.4", "gpt-5.3-codex"):
        g = grader_for(model)
        assert g["id"].endswith("promptfoo/judge.py")
        assert g["config"]["model"] == model


def test_explicit_codex_prefix_is_stripped():
    g = grader_for("codex:gpt-5.4")
    assert g["id"].endswith("promptfoo/judge.py")
    assert g["config"]["model"] == "gpt-5.4"
