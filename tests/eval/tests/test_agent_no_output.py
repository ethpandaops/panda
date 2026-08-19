"""A turn that never reached the model must fail loudly, not score as an answer.

CI hit this for real: every smoke case came back with 0 tokens, 0 tool calls and
an empty answer, and the harness graded them as wrong answers (`crashed=False`,
"0 errors"). That reads as an eval regression when it is actually an
infrastructure failure, and it also skips the provider's retry-on-crash path.
"""

from typing import Any

import pytest

from agent.opencode_agent import OpenCodeAgent, _error_text, _raise_if_no_output
from agent.wrapper import ToolCallRecord
from config.settings import EvalSettings


def test_provider_error_is_raised():
    with pytest.raises(RuntimeError, match="provider error.*model not found"):
        _raise_if_no_output(
            provider_error={"message": "model not found"},
            final_text="",
            tool_calls=[],
            tokens=0,
            model="opencode-go/deepseek-v4-flash",
        )


def test_silent_empty_turn_is_raised():
    with pytest.raises(RuntimeError, match="produced no output"):
        _raise_if_no_output(
            provider_error=None, final_text="", tool_calls=[], tokens=0, model="p/m"
        )


@pytest.mark.parametrize(
    "final_text,tool_calls,tokens",
    [
        ("an answer", [], 0),  # answered, no tools
        ("", [ToolCallRecord(name="t", input={})], 0),  # tools ran, no summary text
        ("", [], 42),  # model billed tokens but said nothing
    ],
)
def test_turn_with_any_signal_is_accepted(final_text, tool_calls, tokens):
    _raise_if_no_output(
        provider_error=None,
        final_text=final_text,
        tool_calls=tool_calls,
        tokens=tokens,
        model="p/m",
    )


@pytest.mark.parametrize(
    "payload,expected",
    [
        ({"message": "boom"}, "boom"),
        ({"detail": "nested"}, "nested"),
        ({"code": 502}, '{"code": 502}'),
        # The shape CI actually returned when deepseek-v4-flash became opt-in.
        ({"data": {"message": "requires explicit opt in"}}, "requires explicit opt in"),
        ("plain", "plain"),
    ],
)
def test_error_text_renders_provider_shapes(payload, expected):
    assert _error_text(payload) == expected


class _FakeSession:
    """Mimics an opencode session; the turn's messages only exist after chat().

    execute() snapshots message ids before the turn and attributes only new ones,
    so the pre-chat read must be empty or every message is filtered out as `seen`.
    """

    def __init__(self, messages: list[dict[str, Any]]) -> None:
        self._messages = messages
        self._chatted = False

    async def create(self):
        return type("S", (), {"id": "sess-1"})()

    async def messages(self, id: str):  # noqa: A002 - matches the SDK signature
        return self._messages if self._chatted else []

    async def chat(self, **kwargs):
        self._chatted = True
        return None


class _FakeClient:
    def __init__(self, messages: list[dict[str, Any]]) -> None:
        self.session = _FakeSession(messages)


async def _run(monkeypatch, messages: list[dict[str, Any]]):
    agent = OpenCodeAgent(EvalSettings())

    async def _noop_ensure_server() -> None:
        return None

    monkeypatch.setattr(agent, "_ensure_server", _noop_ensure_server)
    agent._client = _FakeClient(messages)
    agent._langfuse = None
    return await agent.execute("what datasources are available?")


async def test_execute_marks_empty_turn_as_error(monkeypatch):
    empty_turn = [{"info": {"id": "m1", "role": "assistant"}, "parts": []}]

    result = await _run(monkeypatch, empty_turn)

    assert result.is_error
    assert "produced no output" in (result.error_message or "")
    assert result.output == ""


async def test_execute_surfaces_provider_error(monkeypatch):
    errored = [
        {
            "info": {"id": "m1", "role": "assistant", "error": {"message": "no such model"}},
            "parts": [],
        }
    ]

    result = await _run(monkeypatch, errored)

    assert result.is_error
    assert "no such model" in (result.error_message or "")


async def test_execute_keeps_a_real_answer(monkeypatch):
    answered = [
        {
            "info": {"id": "m1", "role": "assistant", "tokens": {"input": 10, "output": 5}},
            "parts": [{"type": "text", "text": "clickhouse, prometheus"}],
        }
    ]

    result = await _run(monkeypatch, answered)

    assert not result.is_error
    assert result.output == "clickhouse, prometheus"
