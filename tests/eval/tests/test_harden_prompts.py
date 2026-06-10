"""Guard the harden prompt templates against str.format injection: prose edits
that introduce literal braces (e.g. a `datasets://{name}` URI) raise KeyError
at runtime, since the templates are formatted with a fixed argument set."""

from __future__ import annotations

from harden import auditor as auditor_mod
from harden import report as report_mod


def test_auditor_prompt_formats() -> None:
    rendered = auditor_mod._PROMPT.format(questions="- q", diff="diff")

    assert "- q" in rendered
    assert "diff" in rendered


def test_report_rules_carry_no_unescaped_format_fields() -> None:
    # _RULES is concatenated today, but prompts get refactored into templates;
    # formatting it with no arguments must not raise.
    assert report_mod._RULES.format() == report_mod._RULES
