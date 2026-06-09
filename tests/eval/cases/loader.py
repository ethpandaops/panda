"""Test case loader for YAML-defined evaluation cases.

One case shape for everything. A case is single-turn (one ``input``) or multi-turn
(``steps``); the loader flattens steps into ``input`` + ``followups`` (run in one session).
Grading is promptfoo ``assert`` blocks, passed through verbatim.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from pathlib import Path

import yaml


@dataclass
class TestCase:
    """A single evaluation case.

    ``input`` is always the first prompt; ``followups`` carries any additional turns
    (multi-turn cases run them in the same session). ``asserts`` are promptfoo assert
    blocks (llm-rubric / python / ...) handed to the grader verbatim.
    """

    id: str
    input: str
    followups: list[str] = field(default_factory=list)
    description: str = ""
    network: str = "mainnet"
    tags: list[str] = field(default_factory=list)
    skip: bool = False
    skip_reason: str = ""
    asserts: list[dict] = field(default_factory=list)
    # Alternate phrasings of ``input`` with the SAME intent/answer (hydrated once by a
    # frontier model). Each runs as its own case under the same id + asserts, so the harness
    # is graded on intent across wordings — and the proposer can't overfit to one phrasing.
    variations: list[str] = field(default_factory=list)


def load_test_cases(filename: str, cases_dir: Path | None = None) -> list[TestCase]:
    """Load cases from a ``cases/*.yaml`` file.

    Single-turn cases use ``input``; multi-turn cases use ``steps`` (a list of
    ``{prompt: ...}`` or bare strings), which is flattened to input + followups.
    """
    if cases_dir is None:
        cases_dir = Path(__file__).parent

    filepath = cases_dir / filename
    if not filepath.exists():
        raise FileNotFoundError(f"Test case file not found: {filepath}")

    with open(filepath) as f:
        data = yaml.safe_load(f)

    if not isinstance(data, list):
        raise ValueError(f"Expected a list of test cases in {filename}")

    test_cases = []
    for item in data:
        if not isinstance(item, dict):
            raise ValueError(f"Each test case must be a dict, got {type(item)}")
        if item.get("skip", False):
            continue

        input_text = item.get("input", "")
        followups: list[str] = []
        steps = item.get("steps")
        if steps:
            prompts = [(s.get("prompt", "") if isinstance(s, dict) else str(s)) for s in steps]
            prompts = [p for p in prompts if p]
            if prompts:
                input_text, followups = prompts[0], prompts[1:]

        test_cases.append(
            TestCase(
                id=item.get("id", ""),
                input=input_text,
                followups=followups,
                description=item.get("description", ""),
                network=item.get("network", "mainnet"),
                tags=item.get("tags", []),
                skip=item.get("skip", False),
                skip_reason=item.get("skip_reason", ""),
                asserts=item.get("assert", []),
                variations=item.get("variations", []),
            )
        )

    return test_cases


def get_test_case_ids(filename: str, cases_dir: Path | None = None) -> list[str]:
    """IDs of all cases in a file."""
    return [case.id for case in load_test_cases(filename, cases_dir)]
