"""The measured-data types the harness reasons over.

Measurement itself is delegated to promptfoo (see harness.promptfoo_eval): it runs the
cases across our agentic subjects K times, grades them with its asserts, and hands back
runs that get turned into these types. This module is just the vocabulary — a Question, a
RunRecord (raw trace + its score), and a CandidateResult (the measured quality of one
harness state, which the gates compare).
"""

from __future__ import annotations

from dataclasses import dataclass, field

from harness.scoring import RunScore
from harness.trace import RunTrace


@dataclass
class Question:
    """A question to measure, with a stable id for paired scoring.

    Single-turn (just ``text``) or multi-turn (``text`` + ``followups``, run in one
    session). ``asserts`` are promptfoo assert blocks passed through verbatim as the
    grading rubric; empty means a default "did it plausibly answer?" rubric.
    """

    id: str
    text: str
    followups: list[str] = field(default_factory=list)
    asserts: list[dict] = field(default_factory=list)
    # Alternate phrasings of ``text`` (same intent + answer + asserts). Each is measured as
    # its own run under this id, so the question's score spans wordings — a built-in
    # generalization test the proposer can't beat by memorizing one phrasing.
    variations: list[str] = field(default_factory=list)

    @property
    def prompts(self) -> list[str]:
        """The full turn sequence; a 1-element list for single-turn questions."""
        return [self.text, *self.followups]

    @property
    def phrasings(self) -> list[str]:
        """The canonical text plus every variation — each an opening prompt for one run."""
        return [self.text, *self.variations]


@dataclass
class RunRecord:
    """One measured run kept whole: the raw trace plus its score. The proposer reads the
    raw trace; the gates read the score."""

    question: Question
    trace: RunTrace
    score: RunScore


@dataclass
class CandidateResult:
    """The measured quality of one harness state — what gates compare."""

    runs: list[RunScore] = field(default_factory=list)
    records: list[RunRecord] = field(default_factory=list)  # raw traces, for the proposer
    score: float = 0.0  # mean per-run score (the objective)
    pass_rate: float = 0.0  # correctness rate (the no-regression floor)
    by_subject: dict[str, float] = field(default_factory=dict)  # per-subject mean score
    refs: dict[str, float] = field(default_factory=dict)  # per-question token reference used
