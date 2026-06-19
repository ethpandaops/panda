"""grader_for routes judge specs to the right promptfoo grading provider."""

from config.settings import grader_for


def test_bare_model_uses_opencode_go_gateway():
    g = grader_for("qwen3.7-plus")
    assert g["id"] == "openai:chat:qwen3.7-plus"
    assert g["config"]["apiBaseUrl"].endswith("/zen/go/v1")
    # opencode-go path is keyed off an API key, not a file provider.
    assert "apiKeyEnvar" in g["config"]


def test_provider_slash_model_uses_opencode_judge():
    g = grader_for("openai/gpt-5.5")
    assert g["id"].startswith("file://") and g["id"].endswith("promptfoo/judge.py")
    assert g["config"] == {"provider_id": "openai", "model_id": "gpt-5.5"}


def test_explicit_opencode_prefix_is_stripped():
    g = grader_for("opencode:openai/gpt-5.5")
    assert g["config"] == {"provider_id": "openai", "model_id": "gpt-5.5"}


def test_model_id_keeps_remaining_slashes():
    g = grader_for("openai/org/gpt-5.5")
    assert g["config"] == {"provider_id": "openai", "model_id": "org/gpt-5.5"}
