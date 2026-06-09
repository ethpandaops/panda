"""Test case loader for YAML-defined evaluation cases."""

from __future__ import annotations

from dataclasses import dataclass, field
from pathlib import Path

import yaml


@dataclass
class TestCase:
    """A single test case for evaluation.

    A case is single-turn (one ``input``) or multi-turn (``followups`` carries the
    additional prompts, run in the same session). ``input`` is always the first prompt.
    """

    id: str
    input: str
    followups: list[str] = field(default_factory=list)
    description: str = ""
    network: str = "mainnet"
    tags: list[str] = field(default_factory=list)
    skip: bool = False
    skip_reason: str = ""
    # Grading: promptfoo assert blocks, passed through verbatim (llm-rubric / python / ...).
    # Empty -> a default "did it plausibly answer?" rubric.
    asserts: list[dict] = field(default_factory=list)
    # Legacy fields kept only so the older pytest suite still loads; new cases don't set
    # them and the harden loop ignores them (grading is the asserts above).
    expected_tools: list[str] = field(default_factory=list)
    metrics: dict[str, float] = field(default_factory=dict)
    expected_tables: list[str] = field(default_factory=list)
    expected_datasource: str = "clickhouse"
    expected_columns: list[str] = field(default_factory=list)
    require_all_tables: bool = False


@dataclass
class MultiStepStep:
    """A single step in a multi-step test case."""

    prompt: str
    expected_tools: list[str] = field(default_factory=list)
    expect_session_id: bool = False
    use_previous_session: bool = False
    metrics: dict[str, float] = field(default_factory=dict)


@dataclass
class MultiStepTestCase:
    """A multi-step test case with session persistence."""

    id: str
    description: str
    steps: list[MultiStepStep] = field(default_factory=list)
    network: str = "mainnet"
    tags: list[str] = field(default_factory=list)
    skip: bool = False
    skip_reason: str = ""


def load_test_cases(
    filename: str,
    cases_dir: Path | None = None,
) -> list[TestCase]:
    """Load test cases from a YAML file.

    Args:
        filename: Name of the YAML file (e.g., 'basic_queries.yaml').
        cases_dir: Directory containing test case files. Defaults to 'cases/'.

    Returns:
        List of TestCase objects.

    Raises:
        FileNotFoundError: If the YAML file doesn't exist.
        ValueError: If the YAML structure is invalid.
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

        # Handle skip
        if item.get("skip", False):
            continue

        # Unify single-turn (input) and multi-turn (steps) into input + followups.
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
                expected_tools=item.get("expected_tools", []),
                metrics=item.get("metrics", {}),
                expected_tables=item.get("expected_tables", []),
                expected_datasource=item.get("expected_datasource", "clickhouse"),
                expected_columns=item.get("expected_columns", []),
                require_all_tables=item.get("require_all_tables", False),
            )
        )

    return test_cases


def load_multi_step_cases(
    filename: str,
    cases_dir: Path | None = None,
) -> list[MultiStepTestCase]:
    """Load multi-step test cases from a YAML file.

    Args:
        filename: Name of the YAML file (e.g., 'multi_step.yaml').
        cases_dir: Directory containing test case files. Defaults to 'cases/'.

    Returns:
        List of MultiStepTestCase objects.

    Raises:
        FileNotFoundError: If the YAML file doesn't exist.
        ValueError: If the YAML structure is invalid.
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

        # Handle skip
        if item.get("skip", False):
            continue

        steps = []
        for step_data in item.get("steps", []):
            steps.append(
                MultiStepStep(
                    prompt=step_data.get("prompt", ""),
                    expected_tools=step_data.get("expected_tools", []),
                    expect_session_id=step_data.get("expect_session_id", False),
                    use_previous_session=step_data.get("use_previous_session", False),
                    metrics=step_data.get("metrics", {}),
                )
            )

        test_cases.append(
            MultiStepTestCase(
                id=item.get("id", ""),
                description=item.get("description", ""),
                steps=steps,
                network=item.get("network", "mainnet"),
                tags=item.get("tags", []),
                skip=item.get("skip", False),
                skip_reason=item.get("skip_reason", ""),
            )
        )

    return test_cases


def get_test_case_ids(filename: str, cases_dir: Path | None = None) -> list[str]:
    """Get IDs of all test cases in a file (for pytest parametrization).

    Args:
        filename: Name of the YAML file.
        cases_dir: Directory containing test case files.

    Returns:
        List of test case IDs.
    """
    cases = load_test_cases(filename, cases_dir)
    return [case.id for case in cases]
