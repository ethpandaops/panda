#!/usr/bin/env python3
"""Schema probing via self-play: ask the same question N times, check if answers agree."""

from __future__ import annotations

# Must be set before any deepeval imports to suppress Confident AI trace log
import os
os.environ.setdefault("CONFIDENT_API_KEY", "")
os.environ.setdefault("DEEPEVAL_TELEMETRY_OPT_OUT", "YES")

import argparse
import asyncio
import fnmatch
import json
import logging
import sys
import traceback
from dataclasses import asdict
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

# Suppress noisy loggers before any library imports that trigger them
logging.getLogger("deepeval").setLevel(logging.ERROR)
logging.getLogger("confident").setLevel(logging.ERROR)
logging.getLogger("httpx").setLevel(logging.WARNING)

from rich.console import Console
from rich.panel import Panel
from rich.table import Table

console = Console()

PROBES_DIR = Path(__file__).parent.parent / "probes"
RESULTS_DIR = PROBES_DIR / "results"
CASES_FILE = Path(__file__).parent.parent / "cases" / "probes.yaml"


def load_probe_cases(filepath: Path) -> list[dict[str, Any]]:
    """Load probe cases from YAML."""
    import yaml

    if not filepath.exists():
        console.print(f"[red]Probe cases not found: {filepath}[/red]")
        sys.exit(1)

    with open(filepath) as f:
        data = yaml.safe_load(f)

    if not isinstance(data, list):
        console.print("[red]Expected a list of probe cases[/red]")
        sys.exit(1)

    return [c for c in data if isinstance(c, dict) and not c.get("skip", False)]


def get_latest_result() -> dict[str, Any] | None:
    """Load the most recent result file for comparison."""
    if not RESULTS_DIR.exists():
        return None

    result_files = sorted(RESULTS_DIR.glob("*.json"))
    if not result_files:
        return None

    with open(result_files[-1]) as f:
        return json.load(f)


def print_comparison(current: dict[str, Any], previous: dict[str, Any]) -> None:
    """Print a comparison between current and previous run."""
    prev_probes = {p["id"]: p for p in previous.get("probes", [])}
    curr_probes = {p["id"]: p for p in current.get("probes", [])}

    table = Table(title="Comparison with previous run")
    table.add_column("Probe", style="cyan")
    table.add_column("Before", justify="right")
    table.add_column("After", justify="right")
    table.add_column("Delta", justify="right")

    changes = []
    for probe_id, curr in curr_probes.items():
        prev = prev_probes.get(probe_id)
        curr_score = curr["agreement"]["table_agreement"]

        if prev is None:
            changes.append((probe_id, None, curr_score, "NEW"))
            continue

        prev_score = prev["agreement"]["table_agreement"]
        delta = curr_score - prev_score

        if delta > 0.01:
            label = "[green]IMPROVED[/green]"
        elif delta < -0.01:
            label = "[red]REGRESSED[/red]"
        else:
            label = ""

        changes.append((probe_id, prev_score, curr_score, label))

    for probe_id, prev_score, curr_score, label in changes:
        prev_str = f"{prev_score:.2f}" if prev_score is not None else "-"
        delta_val = curr_score - prev_score if prev_score is not None else 0.0
        delta_str = f"{delta_val:+.2f}" if prev_score is not None else "NEW"
        table.add_row(probe_id, prev_str, f"{curr_score:.2f}", f"{delta_str} {label}")

    prev_summary = previous.get("summary", {})
    curr_summary = current.get("summary", {})
    console.print()
    console.print(table)
    console.print(
        f"\n  Previous: {prev_summary.get('full_agreement', '?')} agreed, "
        f"{prev_summary.get('disagreement', '?')} disagreed"
    )
    console.print(
        f"  Current:  {curr_summary.get('full_agreement', '?')} agreed, "
        f"{curr_summary.get('disagreement', '?')} disagreed"
    )


def _format_duration(ms: int) -> str:
    """Format milliseconds as a human-readable duration."""
    if ms < 1000:
        return f"{ms}ms"
    seconds = ms / 1000
    if seconds < 60:
        return f"{seconds:.1f}s"
    minutes = seconds / 60
    return f"{minutes:.1f}m"


PERSONAS = [
    {
        "name": "default",
        "prefix": "",
    },
    {
        "name": "careful",
        "prefix": (
            "Think step by step. Before writing any query, reason about which table "
            "is the most appropriate for this question and why. "
            "Use explicit column names. Add comments explaining your choices.\n\n"
        ),
    },
    {
        "name": "concise",
        "prefix": (
            "Answer as directly as possible. Write a single, clean query. "
            "No unnecessary joins, subqueries, or intermediate steps.\n\n"
        ),
    },
    {
        "name": "explorer",
        "prefix": (
            "Before writing any query, use the search tool to find relevant examples "
            "and documentation. Base your table and column choices on what you find.\n\n"
        ),
    },
    {
        "name": "skeptic",
        "prefix": (
            "Double-check your work. After getting results, verify they make sense. "
            "If the numbers look wrong, try a different approach.\n\n"
        ),
    },
]


def _extract_code_from_tool_calls(tool_calls: list[Any]) -> list[str]:
    """Pull Python code from execute_python tool calls for display."""
    code_blocks = []
    for tc in tool_calls:
        name = tc.name if hasattr(tc, "name") else tc.get("name", "")
        if "execute_python" not in str(name):
            continue
        tool_input = tc.input if hasattr(tc, "input") else tc.get("input", {})
        if isinstance(tool_input, dict):
            code = tool_input.get("code", "")
            if code:
                code_blocks.append(code)
    return code_blocks


async def run_probe(
    case: dict[str, Any],
    attempts: int,
    model: str,
    mcp_url: str,
    verbose: bool = False,
) -> dict[str, Any]:
    """Run a single probe: N independent attempts at the same question."""
    from agent.wrapper import MCPAgent
    from config.settings import EvalSettings
    from probes.analysis import AttemptResult, extract_tables, score_agreement

    question = case["question"]
    probe_id = case["id"]
    attempt_results: list[AttemptResult] = []

    for i in range(attempts):
        persona = PERSONAS[i % len(PERSONAS)]
        console.print(f"  [bold]Attempt {i + 1}/{attempts}[/bold] [dim]({persona['name']})[/dim]")

        settings = EvalSettings(model=model, mcp_url=mcp_url)
        agent = MCPAgent(settings)

        prompt = persona["prefix"] + question

        result = None
        try:
            result = await agent.execute(prompt, test_id=f"probe-{probe_id}-{i + 1}")
            tool_call_dicts = [
                {"name": tc.name, "input": tc.input, "result": tc.result}
                for tc in result.tool_calls
            ]
            tables = await extract_tables(tool_call_dicts)

            attempt = AttemptResult(
                tables=tables,
                persona=persona["name"],
                error=result.is_error,
                error_message=result.error_message,
                cost_usd=result.total_cost_usd or 0.0,
                duration_ms=result.duration_ms,
            )
        except Exception as e:
            error_detail = "".join(traceback.format_exception(e))
            attempt = AttemptResult(persona=persona["name"], error=True, error_message=str(e))
            console.print(f"    [red]EXCEPTION: {e}[/red]")
            if verbose:
                console.print(Panel(error_detail, title="Traceback", border_style="red"))

        attempt_results.append(attempt)

        if attempt.error:
            console.print(f"    [red]ERROR[/red]: {attempt.error_message or 'unknown'}")
            # Show what we got before the error
            if result and result.tool_calls:
                console.print(f"    [dim]Tool calls made: {len(result.tool_calls)}[/dim]")
                for tc in result.tool_calls:
                    is_err = " [red](error)[/red]" if tc.is_error else ""
                    console.print(f"      {tc.name}{is_err}")
            if result and result.output:
                # Show truncated agent output
                output = result.output[:300]
                if len(result.output) > 300:
                    output += "..."
                console.print(f"    [dim]Agent output: {output}[/dim]")
        else:
            tables_str = ", ".join(attempt.tables) or "(no tables detected)"
            n_tools = len(result.tool_calls)
            duration = _format_duration(attempt.duration_ms)
            cost = f"${attempt.cost_usd:.4f}"
            console.print(
                f"    [dim]{duration}  {cost}  {n_tools} tool calls[/dim]  "
                f"tables=[cyan]{tables_str}[/cyan]"
            )

        # Show code in verbose mode (both success and error)
        if verbose and result and result.tool_calls:
            code_blocks = _extract_code_from_tool_calls(result.tool_calls)
            for j, code in enumerate(code_blocks):
                label = f"execute_python call {j + 1}" if len(code_blocks) > 1 else "execute_python"
                lines = code.strip().split("\n")
                if len(lines) > 20:
                    lines = lines[:20] + [f"... ({len(lines) - 20} more lines)"]
                console.print(Panel(
                    "\n".join(lines),
                    title=f"[dim]{label}[/dim]",
                    border_style="dim",
                ))

    agreement = score_agreement(attempt_results)

    if agreement.all_agreed:
        console.print(f"  [green]=> All {attempts} agreed[/green]: {agreement.finding}")
    else:
        console.print(f"  [red]=> Disagreement[/red]: {agreement.finding}")

    return {
        "id": probe_id,
        "question": question,
        "attempts": [asdict(a) for a in attempt_results],
        "agreement": asdict(agreement),
    }


async def run_all_probes(
    cases: list[dict[str, Any]],
    attempts: int,
    model: str,
    mcp_url: str,
    verbose: bool = False,
) -> dict[str, Any]:
    """Run all probes and produce a full result."""
    run_id = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H-%M-%S")
    probe_results: list[dict[str, Any]] = []
    total_cost = 0.0

    for i, case in enumerate(cases):
        console.print(f"\n[bold][{i + 1}/{len(cases)}] {case['id']}[/bold]")
        console.print(f"  [dim]{case['question']}[/dim]")

        result = await run_probe(case, attempts, model, mcp_url, verbose=verbose)
        probe_results.append(result)

        probe_cost = sum(a.get("cost_usd", 0) for a in result["attempts"])
        total_cost += probe_cost

    full_agreement = sum(1 for p in probe_results if p["agreement"]["all_agreed"])
    disagreement = len(probe_results) - full_agreement

    return {
        "run_id": run_id,
        "timestamp": datetime.now(timezone.utc).isoformat(),
        "model": model,
        "attempts_per_probe": attempts,
        "total_probes": len(probe_results),
        "total_cost_usd": round(total_cost, 6),
        "summary": {
            "full_agreement": full_agreement,
            "disagreement": disagreement,
            "agreement_rate": round(full_agreement / len(probe_results), 3)
            if probe_results
            else 0.0,
        },
        "probes": probe_results,
    }


def print_results(results: dict[str, Any]) -> None:
    """Print a rich summary table."""
    table = Table(title="Probe Results")
    table.add_column("Probe", style="cyan")
    table.add_column("Agreement", justify="right")
    table.add_column("Agreed?", justify="center")
    table.add_column("Finding")

    for probe in results["probes"]:
        agreement = probe["agreement"]
        score = agreement["table_agreement"]

        if agreement["all_agreed"]:
            agreed_str = "[green]yes[/green]"
            score_style = "green"
        else:
            agreed_str = "[red]no[/red]"
            score_style = "red"

        table.add_row(
            probe["id"],
            f"[{score_style}]{score:.2f}[/{score_style}]",
            agreed_str,
            agreement["finding"],
        )

    console.print()
    console.print(table)

    summary = results["summary"]
    console.print(
        f"\n  [bold]Summary:[/bold] {summary['full_agreement']} agreed, "
        f"{summary['disagreement']} disagreed "
        f"({summary['agreement_rate']:.0%} agreement rate)"
    )
    console.print(f"  [dim]Cost: ${results['total_cost_usd']:.4f}[/dim]")


def save_results(results: dict[str, Any]) -> Path:
    """Save results to a timestamped file."""
    RESULTS_DIR.mkdir(parents=True, exist_ok=True)

    filepath = RESULTS_DIR / f"{results['run_id']}.json"
    with open(filepath, "w") as f:
        json.dump(results, f, indent=2)

    return filepath


async def main_async(args: argparse.Namespace) -> None:
    """Main async entry point."""
    cases = load_probe_cases(CASES_FILE)

    if args.probe:
        cases = [c for c in cases if fnmatch.fnmatch(c["id"], args.probe)]

    if args.limit:
        cases = cases[:args.limit]

    if not cases:
        console.print("[yellow]No matching probe cases found[/yellow]")
        return

    console.print(
        f"[bold]Running {len(cases)} probes, {args.attempts} attempts each[/bold]"
    )
    console.print(f"  Model: [cyan]{args.model}[/cyan]")
    console.print(f"  Server: [cyan]{args.url}[/cyan]")

    previous = get_latest_result()
    results = await run_all_probes(
        cases, args.attempts, args.model, args.url, verbose=args.verbose
    )

    print_results(results)

    filepath = save_results(results)
    console.print(f"\n  [dim]Results saved: {filepath}[/dim]")

    if previous:
        print_comparison(results, previous)


def main() -> None:
    """CLI entry point."""
    parser = argparse.ArgumentParser(
        description="Schema probing via self-play",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  uv run python -m scripts.run_probes
  uv run python -m scripts.run_probes --attempts 5
  uv run python -m scripts.run_probes --probe "blocks_*"
  uv run python -m scripts.run_probes --model claude-haiku-4-5
        """,
    )
    parser.add_argument(
        "--attempts",
        type=int,
        default=3,
        help="Number of independent attempts per probe (default: 3)",
    )
    parser.add_argument(
        "--probe",
        type=str,
        default=None,
        help="Filter probes by ID glob pattern (e.g., 'blocks_*')",
    )
    parser.add_argument(
        "-n",
        "--limit",
        type=int,
        default=None,
        help="Run only the first N probes",
    )
    parser.add_argument(
        "-v",
        "--verbose",
        action="store_true",
        help="Show generated code and full tracebacks",
    )
    parser.add_argument(
        "--model",
        default="claude-sonnet-4-5",
        help="Claude model to use (default: claude-sonnet-4-5)",
    )
    parser.add_argument(
        "--url",
        default="http://localhost:2480",
        help="Panda server URL (default: http://localhost:2480)",
    )
    args = parser.parse_args()

    try:
        asyncio.run(main_async(args))
    except KeyboardInterrupt:
        console.print("\n[dim]Interrupted[/dim]")
        sys.exit(0)


if __name__ == "__main__":
    main()
