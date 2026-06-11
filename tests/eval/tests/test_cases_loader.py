"""Loader behavior: glob-all default, cross-file duplicate-id guard, tag selection."""

from __future__ import annotations

import textwrap

import pytest

from cases.loader import get_test_case_ids, load_test_cases


def _write(tmp_path, name: str, body: str) -> None:
    (tmp_path / name).write_text(textwrap.dedent(body))


@pytest.fixture
def cases_dir(tmp_path):
    _write(
        tmp_path,
        "alpha.yaml",
        """\
        - id: a1
          input: "question a1"
          tags: [smoke, blocks]
          assert:
            - type: llm-rubric
              value: r
        - id: a2
          input: "question a2"
          tags: [blocks, mev]
          assert:
            - type: llm-rubric
              value: r
        """,
    )
    _write(
        tmp_path,
        "beta.yaml",
        """\
        - id: b1
          input: "question b1"
          tags: [validators]
          assert:
            - type: llm-rubric
              value: r
        - id: b2
          input: "question b2"
          skip: true
          skip_reason: flaky
        """,
    )
    return tmp_path


def test_load_single_file(cases_dir):
    assert [c.id for c in load_test_cases("alpha.yaml", cases_dir)] == ["a1", "a2"]


def test_load_all_files_by_default(cases_dir):
    assert get_test_case_ids(cases_dir=cases_dir) == ["a1", "a2", "b1"]


def test_duplicate_id_across_files_rejected(cases_dir):
    _write(cases_dir, "gamma.yaml", '- id: a1\n  input: "dup"\n')
    with pytest.raises(ValueError, match="duplicate case id 'a1'"):
        load_test_cases(cases_dir=cases_dir)


def test_tags_select_any_match(cases_dir):
    ids = [c.id for c in load_test_cases(cases_dir=cases_dir, tags=["smoke", "validators"])]
    assert ids == ["a1", "b1"]


def test_exclude_tags(cases_dir):
    ids = [c.id for c in load_test_cases(cases_dir=cases_dir, exclude_tags=["mev"])]
    assert ids == ["a1", "b1"]


def test_tags_then_exclude(cases_dir):
    ids = [
        c.id for c in load_test_cases(cases_dir=cases_dir, tags=["blocks"], exclude_tags=["mev"])
    ]
    assert ids == ["a1"]


def test_missing_file_raises(cases_dir):
    with pytest.raises(FileNotFoundError):
        load_test_cases("nope.yaml", cases_dir)
