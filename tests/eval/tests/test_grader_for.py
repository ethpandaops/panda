"""grader_for routes judge specs to the right promptfoo grading provider."""

from config.settings import CODEX_JUDGE_REASONING_EFFORT, grader_for


def test_bare_model_uses_opencode_go_gateway():
    g = grader_for("qwen3.7-plus")
    assert g["id"] == "openai:chat:qwen3.7-plus"
    assert g["config"]["apiBaseUrl"].endswith("/zen/go/v1")
    # opencode-go path is keyed off an API key, not a file provider.
    assert "apiKeyEnvar" in g["config"]


def test_codex_prefix_uses_direct_codex_judge():
    g = grader_for("codex/gpt-5.4")
    assert g["id"].startswith("file://") and g["id"].endswith("promptfoo/judge.py")
    assert g["config"] == {"model": "gpt-5.4", "reasoning_effort": CODEX_JUDGE_REASONING_EFFORT}


def test_codex_prefix_is_stripped_for_any_model():
    for model in ("gpt-5.5", "gpt-5.3-codex"):
        g = grader_for(f"codex/{model}")
        assert g["id"].endswith("promptfoo/judge.py")
        assert g["config"]["model"] == model
        assert g["label"] == f"judge:codex/{model}"


def test_bare_gpt_name_no_longer_routes_to_codex():
    # Without the explicit codex/ prefix, a bare gpt-* name goes through the gateway.
    g = grader_for("gpt-5.5")
    assert g["id"] == "openai:chat:gpt-5.5"
    assert "apiKeyEnvar" in g["config"]


def test_legacy_colon_prefix_no_longer_routes_to_codex():
    # The old codex: (colon) form is no longer a Codex trigger; it falls through to
    # the gateway driver as a literal model name.
    g = grader_for("codex:gpt-5.4")
    assert g["id"] == "openai:chat:codex:gpt-5.4"
    assert "apiKeyEnvar" in g["config"]
