#!/usr/bin/env python3
"""Plot probe results over time."""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

import numpy as np

RESULTS_DIR = Path(__file__).parent.parent / "probes" / "results"


def main() -> None:
    parser = argparse.ArgumentParser(description="Plot probe results over time")
    parser.add_argument(
        "probe_id",
        nargs="?",
        default=None,
        help="Probe ID to plot. Omit for summary dashboard.",
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
    costs = []
    turns = []

    for run in runs:
        for probe in run.get("probes", []):
            if probe["id"] == probe_id:
                timestamps.append(run["run_id"])
                agreements.append(probe["agreement"]["table_agreement"])
                probe_cost = sum(a.get("cost_usd", 0) for a in probe["attempts"])
                probe_turns = sum(a.get("num_turns", 0) for a in probe["attempts"])
                costs.append(probe_cost)
                turns.append(probe_turns)

    if not timestamps:
        print(f"No results found for probe '{probe_id}'")
        sys.exit(1)

    fig, (ax1, ax2, ax3) = plt.subplots(3, 1, figsize=(10, 8), sharex=True)

    x = range(len(timestamps))

    # Agreement
    ax1.plot(x, agreements, "o-", color="#3b82f6", linewidth=2, markersize=6)
    ax1.fill_between(x, agreements, alpha=0.1, color="#3b82f6")
    ax1.axhline(y=1.0, color="#22c55e", linestyle="--", alpha=0.4)
    ax1.set_ylabel("Agreement")
    ax1.set_ylim(-0.05, 1.1)
    ax1.set_title(probe_id)

    # Cost
    ax2.bar(x, costs, color="#f59e0b", alpha=0.7)
    ax2.set_ylabel("Cost ($)")

    # Turns
    ax3.bar(x, turns, color="#8b5cf6", alpha=0.7)
    ax3.set_ylabel("Turns (total)")
    ax3.set_xticks(list(x))
    ax3.set_xticklabels(timestamps, rotation=45, ha="right", fontsize=7)

    plt.tight_layout()
    out = RESULTS_DIR / f"{probe_id}.png"
    plt.savefig(out, dpi=150)
    print(f"Saved: {out}")
    plt.show()


def _plot_summary(runs: list[dict]) -> None:
    import matplotlib.pyplot as plt

    if not runs:
        print("No runs found")
        sys.exit(1)

    timestamps = [r["run_id"] for r in runs]

    # Collect all probe IDs seen across all runs
    all_probe_ids: set[str] = set()
    for run in runs:
        for probe in run.get("probes", []):
            all_probe_ids.add(probe["id"])
    probe_ids = sorted(all_probe_ids)

    # Build grids: agreement, cost, turns (NaN where probe wasn't in that run)
    agreement_grid = np.full((len(probe_ids), len(runs)), float("nan"))
    cost_grid = np.full((len(probe_ids), len(runs)), float("nan"))
    turns_grid = np.full((len(probe_ids), len(runs)), float("nan"))

    for j, run in enumerate(runs):
        for probe in run.get("probes", []):
            i = probe_ids.index(probe["id"])
            agreement_grid[i, j] = probe["agreement"]["table_agreement"]
            cost_grid[i, j] = sum(a.get("cost_usd", 0) for a in probe["attempts"])
            turns_grid[i, j] = sum(a.get("num_turns", 0) for a in probe["attempts"])

    fig, (ax1, ax2, ax3) = plt.subplots(
        1, 3, figsize=(20, max(6, len(probe_ids) * 0.35)),
        gridspec_kw={"width_ratios": [2, 1, 1]},
    )

    # Agreement heatmap
    cmap_agreement = plt.cm.RdYlGn  # type: ignore[attr-defined]
    im1 = ax1.imshow(agreement_grid, aspect="auto", cmap=cmap_agreement, vmin=0, vmax=1, interpolation="nearest")
    ax1.set_title("Table Agreement")
    ax1.set_xticks(range(len(timestamps)))
    ax1.set_xticklabels(timestamps, rotation=45, ha="right", fontsize=6)
    ax1.set_yticks(range(len(probe_ids)))
    ax1.set_yticklabels(probe_ids, fontsize=7)
    fig.colorbar(im1, ax=ax1, shrink=0.6, label="Agreement")

    # Cost heatmap
    im2 = ax2.imshow(cost_grid, aspect="auto", cmap="YlOrRd", interpolation="nearest")
    ax2.set_title("Cost ($)")
    ax2.set_xticks(range(len(timestamps)))
    ax2.set_xticklabels(timestamps, rotation=45, ha="right", fontsize=6)
    ax2.set_yticks([])
    fig.colorbar(im2, ax=ax2, shrink=0.6, label="$")

    # Turns heatmap
    im3 = ax3.imshow(turns_grid, aspect="auto", cmap="YlOrRd", interpolation="nearest")
    ax3.set_title("Turns (total)")
    ax3.set_xticks(range(len(timestamps)))
    ax3.set_xticklabels(timestamps, rotation=45, ha="right", fontsize=6)
    ax3.set_yticks([])
    fig.colorbar(im3, ax=ax3, shrink=0.6, label="turns")

    plt.suptitle("Self-Play Probe Dashboard", fontsize=14, fontweight="bold")
    plt.tight_layout()
    out = RESULTS_DIR / "summary.png"
    plt.savefig(out, dpi=150)
    print(f"Saved: {out}")
    plt.show()


if __name__ == "__main__":
    main()
