"""Project repo eval cases into per-branch Langfuse datasets, and link runs.

``cases/*.yaml`` stays the single source of truth. The Langfuse dataset is just a
projection of it, regenerated every run, so cases never live anywhere but the repo.

Datasets are scoped per git branch so a branch run never clobbers master's items:
- master / main -> ``panda-eval-<category>``
- any branch    -> ``panda-eval-<category>@<branch>``

Langfuse dataset item ids are global, so items are keyed by ``<branch>::<category>::
<case_id>``. Each eval run links its traces to a dataset run named by commit sha
(``GITHUB_SHA``), which is what powers per-case score trends and run-vs-run diffs.
"""

from __future__ import annotations

import os
from datetime import datetime, timezone
from typing import Any


def _sanitize(value: str) -> str:
    """Make a branch name safe for dataset names / item ids (drop slashes)."""
    return value.replace("/", "-").strip() or "local"


def eval_branch() -> str:
    """Resolve the git branch for this run (PR head, ref name, or 'local')."""
    raw = os.environ.get("GITHUB_HEAD_REF") or os.environ.get("GITHUB_REF_NAME") or "local"
    return _sanitize(raw)


def eval_run_name() -> str:
    """Name for this run's dataset run: the commit sha in CI, a timestamp locally."""
    sha = (os.environ.get("GITHUB_SHA") or "")[:8]
    return sha or datetime.now(timezone.utc).strftime("local-%Y%m%dT%H%M%S")


def dataset_name(category: str, branch: str) -> str:
    """Per-branch dataset name; master/main keep the clean canonical name."""
    base = f"panda-eval-{category}"
    return base if branch in ("master", "main") else f"{base}@{branch}"


def _item_id(category: str, case_id: str, branch: str) -> str:
    return f"{branch}::{category}::{case_id}"


def upsert_item(
    langfuse: Any,
    *,
    category: str,
    branch: str,
    case_id: str,
    input_text: str,
    expected_output: str | None = None,
    metadata: dict[str, Any] | None = None,
) -> None:
    """Idempotently sync one repo case into its per-branch dataset (upsert by id)."""
    name = dataset_name(category, branch)
    langfuse.create_dataset(
        name=name,
        metadata={"branch": branch, "category": category, "source": f"cases/{category}.yaml"},
    )
    langfuse.create_dataset_item(
        dataset_name=name,
        id=_item_id(category, case_id, branch),
        input=input_text,
        expected_output=expected_output,
        metadata={**(metadata or {}), "case_id": case_id, "category": category, "branch": branch},
    )


def link_run(
    langfuse: Any,
    *,
    category: str,
    branch: str,
    case_id: str,
    run_name: str,
    trace_id: str,
) -> None:
    """Link an already-recorded trace to this run's dataset run item."""
    langfuse.api.dataset_run_items.create(
        run_name=run_name,
        dataset_item_id=_item_id(category, case_id, branch),
        trace_id=trace_id,
        metadata={"branch": branch},
    )
