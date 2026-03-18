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

EVAL_DIR = Path(__file__).parent.parent
REPO_ROOT = EVAL_DIR.parent.parent
PROBES_DIR = EVAL_DIR / "probes"
RESULTS_DIR = PROBES_DIR / "results"
CASES_FILE = EVAL_DIR / "cases" / "probes.yaml"
PROBE_CONFIG = EVAL_DIR / "config-probe.yaml"
SERVER_BINARY = REPO_ROOT / "panda-server"
PROBE_SERVER_PORT = 2481
PROBE_SERVER_URL = f"http://localhost:{PROBE_SERVER_PORT}"


import signal
import subprocess
import time
import urllib.request


class PandaServer:
    """Manages a local panda-server process for probe runs."""

    def __init__(self) -> None:
        self._proc: subprocess.Popen[bytes] | None = None

    def start(self) -> None:
        if not SERVER_BINARY.exists():
            console.print(
                f"[red]Server binary not found at {SERVER_BINARY}[/red]\n"
                f"  Run: make build"
            )
            sys.exit(1)

        if not PROBE_CONFIG.exists():
            console.print(f"[red]Probe config not found at {PROBE_CONFIG}[/red]")
            sys.exit(1)

        # Check if something is already on our port
        if self._port_open():
            console.print(f"[yellow]Port {PROBE_SERVER_PORT} already in use, assuming server is running[/yellow]")
            return

        console.print(f"[dim]Starting panda-server on :{PROBE_SERVER_PORT}...[/dim]")

        self._proc = subprocess.Popen(
            [str(SERVER_BINARY), "serve", "--config", str(PROBE_CONFIG)],
            cwd=str(REPO_ROOT),
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )

        # Wait for server to be ready (EIP embedding takes ~3min on cold start)
        for i in range(240):
            if self._port_open():
                console.print(f"[green]Server ready on :{PROBE_SERVER_PORT} ({i + 1}s)[/green]")
                return
            if self._proc.poll() is not None:
                console.print(f"[red]Server process exited with code {self._proc.returncode}[/red]")
                sys.exit(1)
            time.sleep(1)
            if i > 0 and i % 10 == 0:
                console.print(f"[dim]  Still waiting... ({i}s)[/dim]")

        console.print("[red]Server failed to start within 240s[/red]")
        self.stop()
        sys.exit(1)

    def stop(self) -> None:
        if self._proc is None:
            return
        console.print("[dim]Stopping panda-server...[/dim]")
        self._proc.send_signal(signal.SIGTERM)
        try:
            self._proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            self._proc.kill()
        self._proc = None
        self._cleanup_containers()

    def _cleanup_containers(self) -> None:
        """Remove sandbox containers created by the probe server."""
        try:
            result = subprocess.run(
                ["docker", "ps", "-aq", "--filter",
                 "label=io.ethpandaops-panda.instance=probe"],
                capture_output=True, text=True, timeout=10,
            )
            container_ids = result.stdout.strip().split("\n")
            container_ids = [c for c in container_ids if c]
            if not container_ids:
                return
            console.print(f"[dim]Cleaning up {len(container_ids)} sandbox containers...[/dim]")
            subprocess.run(
                ["docker", "rm", "-f", *container_ids],
                capture_output=True, timeout=30,
            )
        except Exception:
            pass

    def _port_open(self) -> bool:
        import socket

        try:
            with socket.create_connection(("localhost", PROBE_SERVER_PORT), timeout=2):
                return True
        except (OSError, ConnectionRefusedError):
            return False


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


# Global semaphore — set by main before probes run.
_agent_semaphore: asyncio.Semaphore | None = None

# Progress tracking
_progress_lock = asyncio.Lock()
_agents_completed = 0
_agents_total = 0
_probes_completed = 0
_probes_total = 0


async def _update_progress(probe_id: str, persona: str, done: bool = False) -> None:
    """Print a progress line showing agent completion."""
    global _agents_completed, _probes_completed
    async with _progress_lock:
        if done:
            _agents_completed += 1
        console.print(
            f"\r  [dim]Progress: {_agents_completed}/{_agents_total} agents, "
            f"{_probes_completed}/{_probes_total} probes done[/dim]"
            f"  [dim](latest: {probe_id}/{persona})[/dim]",
        )


async def _run_single_attempt(
    question: str,
    probe_id: str,
    attempt_index: int,
    persona: dict[str, str],
    model: str,
    mcp_url: str,
) -> tuple[Any, Any]:
    """Run a single persona attempt. Returns (AttemptResult, ExecutionResult|None)."""
    from agent.wrapper import MCPAgent
    from config.settings import EvalSettings
    from probes.analysis import AttemptResult, extract_tables

    sem = _agent_semaphore or asyncio.Semaphore(999)
    async with sem:
        settings = EvalSettings(model=model, mcp_url=mcp_url)
        agent = MCPAgent(settings)
        prompt = persona["prefix"] + question

        result = None
        try:
            result = await agent.execute(prompt, test_id=f"probe-{probe_id}-{attempt_index + 1}")
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
                num_turns=result.num_turns,
                num_tool_calls=len(result.tool_calls),
            )
        except Exception as e:
            attempt = AttemptResult(persona=persona["name"], error=True, error_message=str(e))

        await _update_progress(probe_id, persona["name"], done=True)
        return attempt, result


async def run_probe(
    case: dict[str, Any],
    attempts: int,
    model: str,
    mcp_url: str,
    verbose: bool = False,
) -> dict[str, Any]:
    """Run a single probe: N independent attempts at the same question, concurrently."""
    global _probes_completed
    from probes.analysis import score_agreement

    question = case["question"]
    probe_id = case["id"]

    personas_to_run = [PERSONAS[i % len(PERSONAS)] for i in range(attempts)]

    tasks = [
        _run_single_attempt(question, probe_id, i, persona, model, mcp_url)
        for i, persona in enumerate(personas_to_run)
    ]
    results_pairs = await asyncio.gather(*tasks)

    attempt_results = [pair[0] for pair in results_pairs]
    agreement = score_agreement(attempt_results)

    # Print probe summary when it finishes
    async with _progress_lock:
        _probes_completed += 1
        if agreement.all_agreed:
            console.print(f"  [green]AGREED[/green]  {probe_id}: {agreement.finding}")
        else:
            console.print(f"  [red]DISAGREE[/red] {probe_id}: {agreement.finding}")

        if verbose:
            for i, (attempt, result) in enumerate(results_pairs):
                persona = personas_to_run[i]
                if attempt.error:
                    console.print(f"    {persona['name']}: [red]ERROR[/red] {attempt.error_message or 'unknown'}")
                else:
                    tables_str = ", ".join(attempt.tables) or "(none)"
                    duration = _format_duration(attempt.duration_ms)
                    console.print(f"    {persona['name']}: [dim]{duration}[/dim] tables=[cyan]{tables_str}[/cyan]")

                if result and result.tool_calls:
                    code_blocks = _extract_code_from_tool_calls(result.tool_calls)
                    for j, code in enumerate(code_blocks):
                        label = f"execute_python call {j + 1}" if len(code_blocks) > 1 else "execute_python"
                        lines = code.strip().split("\n")
                        if len(lines) > 20:
                            lines = lines[:20] + [f"... ({len(lines) - 20} more lines)"]
                        console.print(Panel(
                            "\n".join(lines),
                            title=f"[dim]{persona['name']}: {label}[/dim]",
                            border_style="dim",
                        ))

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
    """Run all probes concurrently. Agent concurrency is controlled by the global semaphore."""
    global _agents_total, _agents_completed, _probes_total, _probes_completed
    _agents_total = len(cases) * attempts
    _agents_completed = 0
    _probes_total = len(cases)
    _probes_completed = 0

    run_id = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H-%M-%S")
    total_cost = 0.0

    probe_results = list(await asyncio.gather(
        *(run_probe(case, attempts, model, mcp_url, verbose=verbose)
          for case in cases)
    ))

    for result in probe_results:
        total_cost += sum(a.get("cost_usd", 0) for a in result["attempts"])

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

    if args.only_previously_failed:
        previous = get_latest_result()
        if previous:
            failed_ids = {
                p["id"]
                for p in previous.get("probes", [])
                if not p["agreement"]["all_agreed"]
            }
            cases = [c for c in cases if c["id"] in failed_ids]
            console.print(
                f"[dim]Retrying {len(cases)} previously disagreeing probes[/dim]"
            )
        else:
            console.print("[yellow]No previous results to retry from[/yellow]")

    if args.limit:
        import random
        random.shuffle(cases)
        cases = cases[:args.limit]

    if not cases:
        console.print("[yellow]No matching probe cases found[/yellow]")
        return

    server = None
    if args.url:
        mcp_url = args.url
    else:
        server = PandaServer()
        server.start()
        mcp_url = PROBE_SERVER_URL

    try:
        global _agent_semaphore
        _agent_semaphore = asyncio.Semaphore(args.concurrency)

        total_agents = len(cases) * args.attempts
        persona_names = ", ".join(
            PERSONAS[i % len(PERSONAS)]["name"] for i in range(args.attempts)
        )
        console.print(
            f"[bold]Running {len(cases)} probes, {args.attempts} personas each "
            f"({total_agents} agents, max {args.concurrency} concurrent)[/bold]"
        )
        console.print(f"  Personas: [dim]{persona_names}[/dim]")
        console.print(f"  Model: [cyan]{args.model}[/cyan]")
        console.print(f"  Server: [cyan]{mcp_url}[/cyan]")

        previous = get_latest_result()
        results = await run_all_probes(
            cases, args.attempts, args.model, mcp_url, verbose=args.verbose,
        )

        print_results(results)

        filepath = save_results(results)
        console.print(f"\n  [dim]Results saved: {filepath}[/dim]")

        if previous:
            print_comparison(results, previous)
    finally:
        if server:
            server.stop()


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
        default=5,
        help="Number of independent attempts per probe (default: 5)",
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
        "-c",
        "--concurrency",
        type=int,
        default=5,
        help="Max concurrent agents (default: 5)",
    )
    parser.add_argument(
        "--only-previously-failed",
        action="store_true",
        help="Only re-run probes that disagreed in the last run",
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
        default=None,
        help=f"Panda server URL (default: starts local server on :{PROBE_SERVER_PORT})",
    )
    args = parser.parse_args()

    try:
        asyncio.run(main_async(args))
    except KeyboardInterrupt:
        console.print("\n[dim]Interrupted[/dim]")
        sys.exit(0)


if __name__ == "__main__":
    main()
