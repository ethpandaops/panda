#!/usr/bin/env python3
"""Plot probe agreement trends over time."""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

RESULTS_DIR = Path(__file__).parent.parent / "probes" / "results"


def main() -> None:
    parser = argparse.ArgumentParser(description="Plot probe agreement trends")
    parser.add_argument(
        "probe_id",
        nargs="?",
        default=None,
        help="Probe ID to plot (e.g., max_block_size). Omit for overall summary.",
    )
    args = parser.parse_args()

    result_files = sorted(RESULTS_DIR.glob("*.json"))
    if not result_files:
        print("No results found in", RESULTS_DIR)
        sys.exit(1)

    runs = []
    for f in result_files:
        with open(f) as fh:
            runs.append(json.load(fh))

    if args.probe_id:
        _plot_probe(runs, args.probe_id)
    else:
        _plot_summary(runs)


def _plot_probe(runs: list[dict], probe_id: str) -> None:
    import matplotlib.pyplot as plt

    timestamps = []
    agreements = []

    for run in runs:
        for probe in run.get("probes", []):
            if probe["id"] == probe_id:
                timestamps.append(run["run_id"])
                agreements.append(probe["agreement"]["table_agreement"])

    if not timestamps:
        print(f"No results found for probe '{probe_id}'")
        sys.exit(1)

    fig, ax = plt.subplots(figsize=(10, 4))

    ax.plot(range(len(timestamps)), agreements, "o-", color="#3b82f6", linewidth=2, markersize=6)
    ax.fill_between(range(len(timestamps)), agreements, alpha=0.1, color="#3b82f6")
    ax.axhline(y=1.0, color="#22c55e", linestyle="--", alpha=0.4, label="Full agreement")

    ax.set_ylabel("Table Agreement")
    ax.set_title(probe_id)
    ax.set_ylim(-0.05, 1.1)
    ax.set_xticks(range(len(timestamps)))
    ax.set_xticklabels(timestamps, rotation=45, ha="right", fontsize=7)
    ax.legend(loc="lower right", fontsize=8)

    plt.tight_layout()
    out = RESULTS_DIR / f"{probe_id}.png"
    plt.savefig(out, dpi=150)
    print(f"Saved: {out}")
    plt.show()


def _plot_summary(runs: list[dict]) -> None:
    import matplotlib.pyplot as plt

    if len(runs) < 1:
        print("Need at least 1 run")
        sys.exit(1)

    timestamps = [r["run_id"] for r in runs]
    rates = [r["summary"]["agreement_rate"] for r in runs]

    # Collect per-probe trends
    all_probe_ids: set[str] = set()
    for run in runs:
        for probe in run.get("probes", []):
            all_probe_ids.add(probe["id"])

    fig, (ax1, ax2) = plt.subplots(2, 1, figsize=(12, 8), gridspec_kw={"height_ratios": [1, 2]})

    # Top: overall agreement rate
    ax1.plot(range(len(timestamps)), rates, "o-", color="#3b82f6", linewidth=2.5, markersize=7)
    ax1.fill_between(range(len(timestamps)), rates, alpha=0.1, color="#3b82f6")
    ax1.axhline(y=1.0, color="#22c55e", linestyle="--", alpha=0.4)
    ax1.set_ylabel("Agreement Rate")
    ax1.set_title("Overall Agreement Rate")
    ax1.set_ylim(-0.05, 1.1)
    ax1.set_xticks(range(len(timestamps)))
    ax1.set_xticklabels(timestamps, rotation=45, ha="right", fontsize=7)

    for i, rate in enumerate(rates):
        s = runs[i]["summary"]
        ax1.annotate(
            f"{s['full_agreement']}/{s['full_agreement'] + s['disagreement']}",
            (i, rate), textcoords="offset points", xytext=(0, 8),
            ha="center", fontsize=7, color="#6b7280",
        )

    # Bottom: heatmap of per-probe agreement across runs
    probe_ids = sorted(all_probe_ids)
    grid = []
    for pid in probe_ids:
        row = []
        for run in runs:
            score = None
            for probe in run.get("probes", []):
                if probe["id"] == pid:
                    score = probe["agreement"]["table_agreement"]
            row.append(score if score is not None else float("nan"))
        grid.append(row)

    import numpy as np
    grid_arr = np.array(grid)

    cmap = plt.cm.RdYlGn  # type: ignore[attr-defined]
    im = ax2.imshow(grid_arr, aspect="auto", cmap=cmap, vmin=0, vmax=1, interpolation="nearest")

    ax2.set_xticks(range(len(timestamps)))
    ax2.set_xticklabels(timestamps, rotation=45, ha="right", fontsize=7)
    ax2.set_yticks(range(len(probe_ids)))
    ax2.set_yticklabels(probe_ids, fontsize=7)
    ax2.set_title("Per-Probe Agreement")

    fig.colorbar(im, ax=ax2, label="Agreement", shrink=0.8)

    plt.tight_layout()
    out = RESULTS_DIR / "summary.png"
    plt.savefig(out, dpi=150)
    print(f"Saved: {out}")
    plt.show()


if __name__ == "__main__":
    main()
