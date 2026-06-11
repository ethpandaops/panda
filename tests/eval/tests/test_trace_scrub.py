"""scrub_secrets: credential-looking env values must never reach written artifacts."""

from harden.promptfoo_eval import scrub_secrets


def test_redacts_credential_env_values(monkeypatch):
    monkeypatch.setenv("OPENROUTER_API_KEY", "sk-or-v1-abcdef1234567890")
    monkeypatch.setenv("MCP_EVAL_LANGFUSE_SECRET_KEY", "sk-lf-0000-1111-2222")

    text = "ran: env\nOPENROUTER_API_KEY=sk-or-v1-abcdef1234567890\nother=sk-lf-0000-1111-2222"
    scrubbed = scrub_secrets(text)

    assert "sk-or-v1-abcdef1234567890" not in scrubbed
    assert "sk-lf-0000-1111-2222" not in scrubbed
    assert "[redacted:OPENROUTER_API_KEY]" in scrubbed
    assert "[redacted:MCP_EVAL_LANGFUSE_SECRET_KEY]" in scrubbed


def test_short_values_left_alone(monkeypatch):
    monkeypatch.setenv("SOME_KEY", "abc")

    assert scrub_secrets("value is abc here") == "value is abc here"


def test_non_credential_env_untouched(monkeypatch):
    monkeypatch.setenv("GITHUB_WORKSPACE", "/home/runner/work/panda")

    text = "cwd: /home/runner/work/panda"
    assert scrub_secrets(text) == text


def test_longer_values_redacted_first(monkeypatch):
    # An overlapping shorter credential must not shred the longer one into fragments
    # that then survive redaction.
    monkeypatch.setenv("A_TOKEN", "secretpart")
    monkeypatch.setenv("B_TOKEN", "secretpart-extended")

    scrubbed = scrub_secrets("x secretpart-extended y secretpart z")

    assert scrubbed == "x [redacted:B_TOKEN] y [redacted:A_TOKEN] z"
