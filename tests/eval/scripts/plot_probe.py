#!/usr/bin/env python3
"""Plot probe results over time."""

from __future__ import annotations

import argparse
import json
import math
import sys
from collections import Counter
from pathlib import Path

import numpy as np

RESULTS_DIR = Path(__file__).parent.parent / "probes" / "results"


def _compute_entropy(probe: dict) -> float:
    """Compute entropy from probe data, backfilling for older results that lack it."""
    entropy = probe.get("agreement", {}).get("entropy")
    if entropy is not None:
        return entropy

    # Backfill: recompute from attempt table lists
    attempts = probe.get("attempts", [])
    valid = [a for a in attempts if not a.get("error", False)]
    if len(valid) <= 1:
        return 0.0

    table_sets = [frozenset(a.get("tables", [])) for a in valid]
    set_counts = Counter(table_sets)
    n = len(valid)
    entropy = 0.0
    for count in set_counts.values():
        p = count / n
        if p > 0:
            entropy -= p * math.log2(p)
    return round(entropy, 4)


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
    entropies = []
    costs = []
    turns = []

    for run in runs:
        for probe in run.get("probes", []):
            if probe["id"] == probe_id:
                timestamps.append(run["run_id"])
                entropies.append(_compute_entropy(probe))
                probe_cost = sum(a.get("cost_usd", 0) for a in probe["attempts"])
                probe_turns = sum(a.get("num_turns", 0) for a in probe["attempts"])
                costs.append(probe_cost)
                turns.append(probe_turns)

    if not timestamps:
        print(f"No results found for probe '{probe_id}'")
        sys.exit(1)

    fig, (ax1, ax2, ax3) = plt.subplots(3, 1, figsize=(10, 8), sharex=True)

    x = range(len(timestamps))

    # Entropy (lower is better)
    ax1.plot(x, entropies, "o-", color="#ef4444", linewidth=2, markersize=6)
    ax1.fill_between(x, entropies, alpha=0.1, color="#ef4444")
    ax1.axhline(y=0.0, color="#22c55e", linestyle="--", alpha=0.4)
    ax1.set_ylabel("Entropy (lower = better)")
    ax1.set_ylim(-0.05, max(max(entropies) * 1.1, 0.5))
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

    # Build grids (NaN where probe wasn't in that run)
    entropy_grid = np.full((len(probe_ids), len(runs)), float("nan"))
    cost_grid = np.full((len(probe_ids), len(runs)), float("nan"))

    for j, run in enumerate(runs):
        for probe in run.get("probes", []):
            i = probe_ids.index(probe["id"])
            entropy_grid[i, j] = _compute_entropy(probe)
            cost_grid[i, j] = sum(a.get("cost_usd", 0) for a in probe["attempts"])

    # Average entropy per run (only over probes present in that run)
    avg_entropies = []
    for j in range(len(runs)):
        col = entropy_grid[:, j]
        valid = col[~np.isnan(col)]
        avg_entropies.append(float(np.mean(valid)) if len(valid) > 0 else float("nan"))

    fig = plt.figure(figsize=(20, max(8, len(probe_ids) * 0.35 + 3)))
    gs = fig.add_gridspec(2, 2, height_ratios=[1, 3], width_ratios=[3, 1])

    # Top: average entropy trend line
    ax_trend = fig.add_subplot(gs[0, :])
    x = range(len(timestamps))
    ax_trend.plot(x, avg_entropies, "o-", color="#ef4444", linewidth=2, markersize=5)
    ax_trend.fill_between(x, avg_entropies, alpha=0.1, color="#ef4444")
    ax_trend.axhline(y=0.0, color="#22c55e", linestyle="--", alpha=0.4)
    ax_trend.set_ylabel("Avg Entropy")
    ax_trend.set_title("Average Entropy Over Time (lower = better)", fontsize=11)
    ax_trend.set_xticks(list(x))
    ax_trend.set_xticklabels(timestamps, rotation=45, ha="right", fontsize=6)
    ax_trend.set_ylim(bottom=-0.05)

    # Bottom-left: entropy heatmap per probe
    ax_heat = fig.add_subplot(gs[1, 0])
    # Reverse colormap: green=0 (good), red=high (bad)
    cmap = plt.cm.RdYlGn_r  # type: ignore[attr-defined]
    max_entropy = float(np.nanmax(entropy_grid)) if not np.all(np.isnan(entropy_grid)) else 2.32
    im = ax_heat.imshow(entropy_grid, aspect="auto", cmap=cmap, vmin=0, vmax=max_entropy, interpolation="nearest")
    ax_heat.set_title("Per-Probe Entropy")
    ax_heat.set_xticks(range(len(timestamps)))
    ax_heat.set_xticklabels(timestamps, rotation=45, ha="right", fontsize=6)
    ax_heat.set_yticks(range(len(probe_ids)))
    ax_heat.set_yticklabels(probe_ids, fontsize=7)
    fig.colorbar(im, ax=ax_heat, shrink=0.6, label="Entropy")

    # Bottom-right: cost heatmap
    ax_cost = fig.add_subplot(gs[1, 1])
    im2 = ax_cost.imshow(cost_grid, aspect="auto", cmap="YlOrRd", interpolation="nearest")
    ax_cost.set_title("Cost ($)")
    ax_cost.set_xticks(range(len(timestamps)))
    ax_cost.set_xticklabels(timestamps, rotation=45, ha="right", fontsize=6)
    ax_cost.set_yticks([])
    fig.colorbar(im2, ax=ax_cost, shrink=0.6, label="$")

    plt.suptitle("Self-Play Probe Dashboard", fontsize=14, fontweight="bold")
    plt.tight_layout()
    out = RESULTS_DIR / "summary.png"
    plt.savefig(out, dpi=150)
    print(f"Saved: {out}")
    plt.show()


if __name__ == "__main__":
    main()
