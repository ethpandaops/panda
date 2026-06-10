"""Hill-climb progress charts for a harden run.

Every measured state appends one line to ``<run_dir>/history.jsonl`` and re-renders
``<run_dir>/progress.png`` in place, so a long run can be watched live: per-round
candidate score (colored by verdict), the champion best-so-far step line (the hill
climb), the pass-rate floor, and the token cost the efficiency score is built from.

The renderer reads only ``history.jsonl``, so it also works standalone:

    uv run python -m harden.charts <run_dir> [<run_dir> ...]
    uv run python -m harden.charts --reconstruct <run_dir>

``--reconstruct`` rebuilds ``history.jsonl`` for a run that predates this module by
re-scoring the saved promptfoo artifacts (``baseline/`` + ``round*_candidate/``) with
the baseline-frozen token reference, exactly as the loop scored them. Accept/reject
verdicts are not stored in those artifacts, so reconstructed rounds carry
``accepted: null`` and the champion line treats every measured round as a potential
step up — read it as "best score seen", not "best committed".

Chart failures must never kill a run: ``record`` swallows render errors after logging.
"""

from __future__ import annotations

import argparse
import json
import statistics
from datetime import datetime, timezone
from pathlib import Path
from typing import TYPE_CHECKING, Callable

if TYPE_CHECKING:
    from .runner import CandidateResult

HISTORY_NAME = "history.jsonl"
CHART_NAME = "progress.png"


def record(
    run_dir: str | Path,
    *,
    round_n: int,
    label: str,
    accepted: bool | None,
    reason: str,
    result: "CandidateResult | None" = None,
    summary: str = "",
    parent: str = "",
    log: Callable[[str], None] | None = None,
) -> None:
    """Append one round to the run's history and re-render the progress chart.

    ``result=None`` means the round never reached a full measure (audit-blocked,
    prescreen reject): it is plotted as an unmeasured marker, not a score point.
    ``accepted=None`` marks a verdict-less entry (the baseline, or reconstruction).
    """
    entry: dict = {
        "ts": datetime.now(timezone.utc).isoformat(timespec="seconds"),
        "round": round_n,
        "label": label,
        "accepted": accepted,
        "reason": reason,
        "measured": result is not None,
        "parent": parent,
        "summary": " ".join(summary.split())[:300],
    }
    if result is not None:
        correct_tokens = [r.tokens for r in result.runs if r.correct and r.tokens > 0]
        entry.update(
            score=round(result.score, 4),
            pass_rate=round(result.pass_rate, 4),
            mean_tokens_correct=round(statistics.mean(correct_tokens), 1)
            if correct_tokens
            else 0.0,
        )
    try:
        path = Path(run_dir) / HISTORY_NAME
        with open(path, "a") as f:
            f.write(json.dumps(entry) + "\n")
        render(run_dir)
    except Exception as exc:  # noqa: BLE001 - a chart must never kill a run
        if log:
            log(f"[yellow]chart update failed[/yellow] ({type(exc).__name__}: {exc})")


def load_history(run_dir: str | Path) -> list[dict]:
    path = Path(run_dir) / HISTORY_NAME
    if not path.exists():
        return []
    return [json.loads(line) for line in path.read_text().splitlines() if line.strip()]


def render(run_dir: str | Path, history: list[dict] | None = None) -> Path | None:
    """Render ``progress.png`` from the run's history. Returns the path, or None if
    there is nothing to plot yet."""
    history = history if history is not None else load_history(run_dir)
    measured = [h for h in history if h.get("measured")]
    if not measured:
        return None

    import matplotlib

    matplotlib.use("Agg")
    import matplotlib.pyplot as plt

    unmeasured = [h for h in history if not h.get("measured")]
    baseline = next((h for h in measured if h["round"] == 0), measured[0])

    fig, (ax_score, ax_pass, ax_tok) = plt.subplots(
        3, 1, figsize=(9, 9), sharex=True, height_ratios=[2, 1, 1]
    )
    fig.suptitle(f"harden {Path(run_dir).name}", fontsize=11)

    # Champion best-so-far: the hill climb. Verdict-less measured rounds (baseline,
    # reconstruction) count as steps; explicit rejects don't.
    best = baseline.get("score", 0.0)
    xs, ys = [baseline["round"]], [best]
    for h in measured:
        if h["round"] == baseline["round"]:
            continue
        if h.get("accepted") is not False and h.get("score", 0.0) > best:
            best = h["score"]
        xs.append(h["round"])
        ys.append(best)
    ax_score.step(xs, ys, where="post", color="black", lw=1.2, ls="--", label="champion (best so far)")

    styles = {
        True: dict(color="tab:green", marker="o", label="accepted"),
        False: dict(color="tab:red", marker="o", label="rejected"),
        None: dict(color="tab:blue", marker="o", label="measured (no verdict)"),
    }
    seen_labels: set[str] = set()
    for h in measured:
        st = styles[h.get("accepted")]
        label = st["label"] if st["label"] not in seen_labels else None
        seen_labels.add(st["label"])
        ax_score.scatter(h["round"], h["score"], color=st["color"], marker=st["marker"], s=45, zorder=3, label=label)
    for h in unmeasured:
        label = "blocked before measure" if "blocked" not in seen_labels else None
        seen_labels.add("blocked")
        ax_score.scatter(h["round"], baseline.get("score", 0.0), color="grey", marker="x", s=45, zorder=3, label=label)
        ax_score.annotate(
            h.get("reason", "")[:18], (h["round"], baseline.get("score", 0.0)),
            textcoords="offset points", xytext=(0, -12), fontsize=7, color="grey", ha="center",
        )
    ax_score.axhline(baseline.get("score", 0.0), color="grey", lw=0.8, alpha=0.5)
    ax_score.annotate(
        f"{best:.3f}", (xs[-1], best), textcoords="offset points", xytext=(6, 4), fontsize=9
    )
    ax_score.set_ylabel("score (token-efficiency,\ncorrectness-gated)")
    ax_score.legend(fontsize=8, loc="lower right")
    ax_score.grid(alpha=0.25)

    for h in measured:
        ax_pass.scatter(
            h["round"], h["pass_rate"], color=styles[h.get("accepted")]["color"], s=40, zorder=3
        )
    ax_pass.axhline(baseline.get("pass_rate", 0.0), color="grey", lw=0.8, alpha=0.5, label="baseline")
    ax_pass.set_ylabel("pass rate")
    ax_pass.set_ylim(-0.05, 1.05)
    ax_pass.legend(fontsize=8, loc="lower right")
    ax_pass.grid(alpha=0.25)

    for h in measured:
        ax_tok.scatter(
            h["round"],
            h.get("mean_tokens_correct", 0.0),
            color=styles[h.get("accepted")]["color"],
            s=40,
            zorder=3,
        )
    ax_tok.axhline(
        baseline.get("mean_tokens_correct", 0.0), color="grey", lw=0.8, alpha=0.5, label="baseline"
    )
    ax_tok.set_ylabel("mean tokens\n(correct runs)")
    ax_tok.set_xlabel("round (0 = baseline)")
    ax_tok.xaxis.get_major_locator().set_params(integer=True)
    ax_tok.legend(fontsize=8, loc="upper right")
    ax_tok.grid(alpha=0.25)

    out = Path(run_dir) / CHART_NAME
    fig.tight_layout()
    fig.savefig(out, dpi=110)
    plt.close(fig)
    return out


def reconstruct(run_dir: str | Path) -> list[dict]:
    """Rebuild ``history.jsonl`` from a finished run's promptfoo artifacts, scoring each
    measured state against the baseline-frozen token reference exactly as the loop did.
    Verdicts are not recoverable from artifacts, so every entry has ``accepted: null``."""
    from .promptfoo_eval import _parse, score_runs, token_reference

    run_dir = Path(run_dir)
    base_results = run_dir / "baseline" / "pf_results.json"
    if not base_results.exists():
        raise FileNotFoundError(f"{base_results} not found — not a harden run dir?")

    history_path = run_dir / HISTORY_NAME
    if history_path.exists():
        history_path.unlink()

    base_runs = _parse(base_results, run_dir / "baseline")
    refs = token_reference(base_runs)
    states: list[tuple[int, str, Path]] = [(0, "baseline", run_dir / "baseline")]
    candidates = sorted(
        run_dir.glob("round*_candidate"),
        key=lambda p: int(p.name.removeprefix("round").removesuffix("_candidate")),
    )
    states += [
        (int(p.name.removeprefix("round").removesuffix("_candidate")), p.name, p)
        for p in candidates
    ]

    for round_n, label, state_dir in states:
        pf_runs = _parse(state_dir / "pf_results.json", state_dir)
        result = score_runs(pf_runs, [], refs=refs)
        record(
            run_dir,
            round_n=round_n,
            label=label,
            accepted=None,
            reason="reconstructed",
            result=result,
        )
    return load_history(run_dir)


def main() -> None:
    ap = argparse.ArgumentParser(description="Render harden hill-climb charts from history.jsonl")
    ap.add_argument("run_dirs", nargs="+", help="harden run directories")
    ap.add_argument(
        "--reconstruct",
        action="store_true",
        help="rebuild history.jsonl from promptfoo artifacts first (for runs that predate charts)",
    )
    args = ap.parse_args()
    for run_dir in args.run_dirs:
        if args.reconstruct:
            reconstruct(run_dir)
        out = render(run_dir)
        print(out or f"{run_dir}: nothing to plot (no history)")


if __name__ == "__main__":
    main()
