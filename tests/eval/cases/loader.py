"""Test case loader for YAML-defined evaluation cases.

One case shape for everything. A case is single-turn (one ``input``) or multi-turn
(``steps``); the loader flattens steps into ``input`` + ``followups`` (run in one session).
Grading is promptfoo ``assert`` blocks, passed through verbatim.

Files under ``cases/`` are purely organizational (one domain per file); selection is by
tags. ``load_test_cases()`` with no filename loads every ``cases/*.yaml`` (case ids must
be unique across files); ``tags``/``exclude_tags`` filter the result.

Tags are drawn from a fixed corpus so ``--tags`` selection stays predictable. New cases
must reuse these, not invent new ones:

  Domain (what the question is about):
    blocks         block production, propagation, size, parents, reorgs
    blobs          blobs & data columns (PeerDAS): counts, getBlobs, gossip
    mev            relays, builders, bid value, timing games
    execution      EL/EVM: precompiles, mempool, fees, engine API
    validators     validator set, duties, head accuracy
    attestations   attestation volume / correctness (cross-cuts validators)
    networks       devnet/testnet discovery, node coverage, finality, peers, pipelines

  Capability / shape (cross-cutting):
    smoke          fast end-to-end sanity checks (the CI smoke set)
    clickhouse     answered by querying the clickhouse datasource
    timing         latency / arrival-time analysis
    multi_step     multi-turn session (uses ``steps``)
    visualization  produces a chart / plot
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
    # is graded on intent across many wordings — and the proposer can't overfit to one
    # phrasing.
    variations: list[str] = field(default_factory=list)


def _load_file(filepath: Path) -> list[TestCase]:
    with open(filepath) as f:
        data = yaml.safe_load(f)

    if not isinstance(data, list):
        raise ValueError(f"Expected a list of test cases in {filepath.name}")

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


def load_test_cases(
    filename: str | None = None,
    cases_dir: Path | None = None,
    *,
    tags: list[str] | None = None,
    exclude_tags: list[str] | None = None,
) -> list[TestCase]:
    """Load cases from one ``cases/*.yaml`` file, or all of them.

    With ``filename=None`` every ``*.yaml`` in ``cases_dir`` is loaded (sorted by name)
    and case ids must be unique across files. ``tags`` keeps only cases carrying at least
    one of the given tags; ``exclude_tags`` then drops cases carrying any of those.

    Single-turn cases use ``input``; multi-turn cases use ``steps`` (a list of
    ``{prompt: ...}`` or bare strings), which is flattened to input + followups.
    """
    if cases_dir is None:
        cases_dir = Path(__file__).parent

    if filename is not None:
        filepath = cases_dir / filename
        if not filepath.exists():
            raise FileNotFoundError(f"Test case file not found: {filepath}")
        cases = _load_file(filepath)
    else:
        cases = []
        seen: dict[str, str] = {}
        for filepath in sorted(cases_dir.glob("*.yaml")):
            for case in _load_file(filepath):
                if case.id in seen:
                    raise ValueError(
                        f"duplicate case id {case.id!r}: in both "
                        f"{seen[case.id]} and {filepath.name}"
                    )
                seen[case.id] = filepath.name
                cases.append(case)

    if tags:
        wanted = set(tags)
        cases = [c for c in cases if wanted & set(c.tags)]
    if exclude_tags:
        unwanted = set(exclude_tags)
        cases = [c for c in cases if not (unwanted & set(c.tags))]
    return cases


def get_test_case_ids(filename: str | None = None, cases_dir: Path | None = None) -> list[str]:
    """IDs of all cases in a file (or across all files)."""
    return [case.id for case in load_test_cases(filename, cases_dir)]
