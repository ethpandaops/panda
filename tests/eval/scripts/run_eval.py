#!/usr/bin/env python3
"""CLI runner for ethpandaops-panda evaluation."""

from __future__ import annotations

import argparse
import os
import subprocess
import sys
from pathlib import Path

from rich.console import Console
from rich.panel import Panel
from rich.table import Table

# Add parent to path for imports
sys.path.insert(0, str(Path(__file__).parent.parent))

from config.settings import DEFAULT_AGENT_MODEL, DEFAULT_EVALUATOR_MODEL

console = Console()


def print_header(
    model: str,
    evaluator_model: str,
    category: str | None,
    verbose: bool,
) -> None:
    """Print evaluation header."""
    console.print()
    console.print(
        Panel(
            f"[bold]ethpandaops-panda Evaluation[/bold]\n\n"
            f"Agent Model: [cyan]{model}[/cyan]\n"
            f"Evaluator Model: [cyan]{evaluator_model}[/cyan]\n"
            f"Category: [cyan]{category or 'all'}[/cyan]\n"
            f"Verbose: [cyan]{verbose}[/cyan]",
            title="Configuration",
            border_style="blue",
        )
    )
    console.print()


def run_pytest(
    model: str,
    evaluator_model: str,
    category: str | None,
    verbose: bool,
    track_costs: bool,
    save_traces: bool = True,
    markers: list[str] | None = None,
    extra_args: list[str] | None = None,
) -> int:
    """Run pytest with the specified configuration.

    Args:
        model: Claude model to use for agent.
        evaluator_model: Model to use for LLM-judged metrics.
        category: Test category to run (or None for all).
        verbose: Whether to enable verbose output.
        track_costs: Whether to track costs.
        save_traces: Whether to save traces locally.
        markers: pytest markers to select tests.
        extra_args: Additional arguments to pass to pytest.

    Returns:
        Exit code from pytest.
    """
    # Set environment variables for the settings
    env = os.environ.copy()
    env["MCP_EVAL_MODEL"] = model
    env["MCP_EVAL_EVALUATOR_MODEL"] = evaluator_model
    env["MCP_EVAL_VERBOSE"] = str(verbose).lower()
    env["MCP_EVAL_TRACK_COSTS"] = str(track_costs).lower()
    env["MCP_EVAL_SAVE_TRACES"] = str(save_traces).lower()

    # Build pytest arguments
    pytest_args = ["pytest", "tests/"]

    if verbose:
        pytest_args.append("-v")
        pytest_args.append("-s")  # Show print output

    if category:
        pytest_args.extend(["-k", category])

    if markers:
        marker_expr = " or ".join(markers)
        pytest_args.extend(["-m", marker_expr])

    if extra_args:
        pytest_args.extend(extra_args)

    # Run pytest
    console.print(f"[dim]Running: {' '.join(pytest_args)}[/dim]")
    console.print()

    result = subprocess.run(
        pytest_args,
        env=env,
        cwd=Path(__file__).parent.parent,
    )

    return result.returncode


def run_deepeval(
    model: str,
    evaluator_model: str,
    category: str | None,
    verbose: bool,
    track_costs: bool,
    save_traces: bool = True,
    confident: bool = False,
) -> int:
    """Run evaluation using deepeval CLI.

    Args:
        model: Claude model to use for agent.
        evaluator_model: Model to use for LLM-judged metrics.
        category: Test category to run (or None for all).
        verbose: Whether to enable verbose output.
        track_costs: Whether to track costs.
        save_traces: Whether to save traces locally.
        confident: Whether to sync results to Confident AI dashboard.

    Returns:
        Exit code from deepeval.
    """
    # Set environment variables for the settings
    env = os.environ.copy()
    env["MCP_EVAL_MODEL"] = model
    env["MCP_EVAL_EVALUATOR_MODEL"] = evaluator_model
    env["MCP_EVAL_VERBOSE"] = str(verbose).lower()
    env["MCP_EVAL_TRACK_COSTS"] = str(track_costs).lower()
    env["MCP_EVAL_SAVE_TRACES"] = str(save_traces).lower()

    # Disable Confident AI sync unless explicitly requested
    if not confident:
        env["CONFIDENT_API_KEY"] = ""

    # Build deepeval arguments
    deepeval_args = ["deepeval", "test", "run", "tests/"]

    if category:
        deepeval_args.extend(["-k", category])

    # Run deepeval
    console.print(f"[dim]Running: {' '.join(deepeval_args)}[/dim]")
    console.print()

    result = subprocess.run(
        deepeval_args,
        env=env,
        cwd=Path(__file__).parent.parent,
    )

    return result.returncode


def list_test_cases() -> None:
    """List all available test cases."""
    from cases.loader import load_multi_step_cases, load_test_cases

    console.print()
    console.print("[bold]Available Test Cases[/bold]")
    console.print()

    # Basic queries
    try:
        basic = load_test_cases("basic_queries.yaml")
        table = Table(title="Basic Queries")
        table.add_column("ID", style="cyan")
        table.add_column("Description", style="dim")
        table.add_column("Network")
        table.add_column("Tags")

        for tc in basic:
            table.add_row(
                tc.id,
                tc.description[:50] + "..." if len(tc.description) > 50 else tc.description,
                tc.network,
                ", ".join(tc.tags[:3]),
            )

        console.print(table)
        console.print()
    except FileNotFoundError:
        console.print("[yellow]basic_queries.yaml not found[/yellow]")

    # Visualizations
    try:
        viz = load_test_cases("visualizations.yaml")
        table = Table(title="Visualizations")
        table.add_column("ID", style="cyan")
        table.add_column("Description", style="dim")
        table.add_column("Tags")

        for tc in viz:
            table.add_row(
                tc.id,
                tc.description[:50] + "..." if len(tc.description) > 50 else tc.description,
                ", ".join(tc.tags[:3]),
            )

        console.print(table)
        console.print()
    except FileNotFoundError:
        console.print("[yellow]visualizations.yaml not found[/yellow]")

    # Multi-step
    try:
        multi = load_multi_step_cases("multi_step.yaml")
        table = Table(title="Multi-Step Sessions")
        table.add_column("ID", style="cyan")
        table.add_column("Description", style="dim")
        table.add_column("Steps")

        for tc in multi:
            table.add_row(
                tc.id,
                tc.description[:50] + "..." if len(tc.description) > 50 else tc.description,
                str(len(tc.steps)),
            )

        console.print(table)
        console.print()
    except FileNotFoundError:
        console.print("[yellow]multi_step.yaml not found[/yellow]")


def main() -> None:
    """Main entry point for CLI runner."""
    parser = argparse.ArgumentParser(
        description="Run ethpandaops-panda LLM evaluation",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  # Run all tests with default model (Sonnet 4.5)
  uv run python -m scripts.run_eval

  # Run with specific model
  uv run python -m scripts.run_eval --model claude-opus-4-5

  # Run specific test category
  uv run python -m scripts.run_eval --category basic_queries -v

  # Run only visualization tests
  uv run python -m scripts.run_eval --markers visualization

  # List all test cases
  uv run python -m scripts.run_eval --list

  # Use DeepEval CLI
  uv run python -m scripts.run_eval --deepeval

  # Sync results to Confident AI dashboard
  uv run python -m scripts.run_eval --deepeval --confident

  # Use GPT-4 as evaluator instead of Gemini
  uv run python -m scripts.run_eval --evaluator-model gpt-4o

  # Use any OpenRouter model as evaluator
  uv run python -m scripts.run_eval --evaluator-model anthropic/claude-3.5-sonnet

  # Or use the installed script
  uv run panda-eval --model claude-sonnet-4-5
        """,
    )

    parser.add_argument(
        "--model",
        default=DEFAULT_AGENT_MODEL,
        choices=["claude-sonnet-4-5", "claude-opus-4-5", "claude-haiku-4-5"],
        help=f"Claude model for the agent (default: {DEFAULT_AGENT_MODEL})",
    )
    parser.add_argument(
        "--evaluator-model",
        default=DEFAULT_EVALUATOR_MODEL,
        help=f"Model for LLM-judged metrics (default: {DEFAULT_EVALUATOR_MODEL}). "
        "Supports OpenRouter (provider/model), OpenAI (gpt-*), Anthropic (claude-*)",
    )
    parser.add_argument(
        "--category",
        "-k",
        help="Test category or pattern to run",
    )
    parser.add_argument(
        "--markers",
        "-m",
        nargs="+",
        help="pytest markers to select tests (e.g., visualization multi_step)",
    )
    parser.add_argument(
        "-v",
        "--verbose",
        action="store_true",
        help="Enable verbose output",
    )
    parser.add_argument(
        "--no-cost",
        action="store_true",
        help="Disable cost tracking",
    )
    parser.add_argument(
        "--no-traces",
        action="store_true",
        help="Disable saving local traces",
    )
    parser.add_argument(
        "--list",
        action="store_true",
        help="List all available test cases and exit",
    )
    parser.add_argument(
        "--deepeval",
        action="store_true",
        help="Use deepeval CLI instead of pytest",
    )
    parser.add_argument(
        "--confident",
        action="store_true",
        help="Sync results to Confident AI dashboard (requires deepeval login)",
    )
    parser.add_argument(
        "extra_args",
        nargs="*",
        help="Additional arguments to pass to pytest",
    )

    args = parser.parse_args()

    # Handle --list
    if args.list:
        list_test_cases()
        sys.exit(0)

    # Print header
    print_header(args.model, args.evaluator_model, args.category, args.verbose)

    # Run evaluation
    if args.deepeval:
        exit_code = run_deepeval(
            model=args.model,
            evaluator_model=args.evaluator_model,
            category=args.category,
            verbose=args.verbose,
            track_costs=not args.no_cost,
            save_traces=not args.no_traces,
            confident=args.confident,
        )
    else:
        exit_code = run_pytest(
            model=args.model,
            evaluator_model=args.evaluator_model,
            category=args.category,
            verbose=args.verbose,
            track_costs=not args.no_cost,
            save_traces=not args.no_traces,
            markers=args.markers,
            extra_args=args.extra_args,
        )

    sys.exit(exit_code)


if __name__ == "__main__":
    main()
