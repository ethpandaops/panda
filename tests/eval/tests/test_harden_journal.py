"""Unit tests for harden.journal — the cross-run proposer memory.

The two behaviors that matter: a rejected patch's fingerprint must survive into the
next run (so the loop can refuse an exact resubmission), and the fingerprint must be
robust to cosmetic diff drift (hunk offsets, blob hashes) while still distinguishing
different changes.
"""

from __future__ import annotations

from harden.journal import Journal, backfill, patch_fingerprint

PATCH_A = """diff --git a/x.go b/x.go
index 1111111..2222222 100644
--- a/x.go
+++ b/x.go
@@ -10,3 +10,4 @@ func f() {
 	a := 1
+	b := 2
 	use(a)
"""

# The same logical change after the surrounding file drifted: different blob hashes
# and hunk offsets, identical content lines.
PATCH_A_DRIFTED = """diff --git a/x.go b/x.go
index 3333333..4444444 100644
--- a/x.go
+++ b/x.go
@@ -42,3 +42,4 @@ func f() {
 	a := 1
+	b := 2
 	use(a)
"""

PATCH_B = PATCH_A.replace("b := 2", "c := 3")


def test_fingerprint_ignores_offsets_and_hashes_but_not_content():
    assert patch_fingerprint(PATCH_A) == patch_fingerprint(PATCH_A_DRIFTED)
    assert patch_fingerprint(PATCH_A) != patch_fingerprint(PATCH_B)


def _journal(tmp_path, context="coverage.yaml"):
    return Journal(tmp_path / "journal.jsonl", context=context)


def test_append_and_rejected_fingerprints_round_trip(tmp_path):
    j = _journal(tmp_path)
    j.append(run="r1", round_n=1, accepted=False, reason="audit-blocked",
             summary="bad idea", fingerprint=patch_fingerprint(PATCH_A))
    j.append(run="r1", round_n=2, accepted=True, reason="champion",
             summary="good idea", score_before=0.2, score_after=0.4,
             fingerprint=patch_fingerprint(PATCH_B))

    # A fresh Journal over the same file sees the same state (cross-run persistence).
    j2 = _journal(tmp_path)
    assert j2.rejected_fingerprints() == {patch_fingerprint(PATCH_A)}
    entries = j2.entries()
    assert entries[0]["cases"] == "coverage.yaml"
    assert entries[1]["score_after"] == 0.4


def test_render_marks_champions_and_reasons(tmp_path):
    j = _journal(tmp_path)
    assert j.render() == ""
    j.append(run="runA", round_n=1, accepted=False, reason="rubric-leak", summary="leaky")
    j.append(run="runA", round_n=2, accepted=True, reason="champion",
             summary="placeholder examples", score_before=0.24, score_after=0.37)
    text = j.render()
    assert "CHAMPION 0.240->0.370: placeholder examples" in text
    assert "rubric-leak: leaky" in text
    # Most recent first.
    assert text.index("CHAMPION") < text.index("rubric-leak")


def test_render_caps_entries_and_chars(tmp_path):
    j = _journal(tmp_path)
    for i in range(40):
        j.append(run="r", round_n=i, accepted=False, reason="x", summary=f"attempt {i}")
    text = j.render(max_entries=5)
    assert "attempt 39" in text and "attempt 34" not in text
    tiny = j.render(max_entries=40, max_chars=200)
    assert len(tiny) < 600 and "truncated" in tiny


def test_backfill_imports_history_with_patch_fingerprints(tmp_path):
    run_dir = tmp_path / "2026-06-11T00-00-00"
    run_dir.mkdir()
    (run_dir / "history.jsonl").write_text(
        '{"round": 0, "label": "baseline", "measured": true, "score": 0.2}\n'
        '{"round": 1, "label": "round1", "accepted": false, "reason": "regression",'
        ' "measured": true, "score": 0.1, "summary": "made it worse"}\n'
    )
    (run_dir / "round1.patch").write_text(PATCH_A)
    j = _journal(tmp_path, context="smoke.yaml")
    assert backfill(run_dir, j) == 1  # the baseline is not a proposal
    assert j.rejected_fingerprints() == {patch_fingerprint(PATCH_A)}
    assert j.entries()[0]["run"] == "2026-06-11T00-00-00"
