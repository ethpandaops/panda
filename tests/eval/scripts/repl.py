#!/usr/bin/env python3
"""Interactive REPL for ad-hoc testing of ethpandaops-panda."""

from __future__ import annotations

import argparse
import asyncio
import sys
from typing import TYPE_CHECKING

from rich.console import Console
from rich.markdown import Markdown
from rich.panel import Panel
from rich.table import Table

if TYPE_CHECKING:
    from agent.wrapper import ExecutionResult


console = Console()


def print_result(result: "ExecutionResult", verbose: bool = False) -> None:
    """Pretty print an execution result."""
    # Print the output
    if result.output:
        console.print(Markdown(result.output))
    elif result.is_error:
        console.print(f"[red]Error: {result.error_message}[/red]")
    else:
        console.print("[yellow]No output[/yellow]")

    # Print verbose info if enabled
    if verbose:
        console.print()
        table = Table(show_header=False, box=None, padding=(0, 1))
        table.add_column("Key", style="dim")
        table.add_column("Value")

        table.add_row("Cost", f"${result.total_cost_usd or 0:.6f}")
        table.add_row("Tokens", f"{result.input_tokens} in / {result.output_tokens} out")
        table.add_row("Duration", f"{result.duration_ms}ms")
        table.add_row("Turns", str(result.num_turns))
        table.add_row("Tools", str(len(result.tool_calls)))

        if result.session_id:
            table.add_row("Session", result.session_id[:20] + "...")

        console.print(Panel(table, title="[dim]Stats[/dim]", border_style="dim"))


def print_welcome(model: str, mcp_url: str) -> None:
    """Print welcome message."""
    console.print()
    console.print(
        Panel(
            f"[bold]ethpandaops-panda REPL[/bold]\n\n"
            f"Model: [cyan]{model}[/cyan]\n"
            f"Server: [cyan]{mcp_url}[/cyan]\n\n"
            f"Commands:\n"
            f"  [green]/new[/green]    - Start new session\n"
            f"  [green]/cost[/green]   - Show session costs\n"
            f"  [green]/tools[/green]  - Show tool calls\n"
            f"  [green]/help[/green]   - Show help\n"
            f"  [green]/quit[/green]   - Exit REPL",
            title="Welcome",
            border_style="blue",
        )
    )
    console.print()


def print_help() -> None:
    """Print help message."""
    console.print(
        Panel(
            "[bold]Available Commands[/bold]\n\n"
            "[green]/new[/green]     Start a new session (clears history)\n"
            "[green]/cost[/green]    Show cumulative session costs\n"
            "[green]/tools[/green]   Show all tool calls in current session\n"
            "[green]/clear[/green]   Clear the screen\n"
            "[green]/verbose[/green] Toggle verbose mode\n"
            "[green]/help[/green]    Show this help message\n"
            "[green]/quit[/green]    Exit the REPL\n\n"
            "[bold]Tips[/bold]\n"
            "- Multi-line input: end a line with \\ to continue\n"
            "- Session state persists across queries\n"
            "- Use verbose mode to see token/cost details",
            title="Help",
            border_style="green",
        )
    )


async def repl_loop(
    model: str,
    verbose: bool,
    mcp_url: str,
) -> None:
    """Main REPL loop.

    Args:
        model: Claude model to use.
        verbose: Whether to show verbose output.
        mcp_url: URL of the ethpandaops-panda server.
    """
    # Import here to avoid circular imports and allow settings override
    from agent.wrapper import ExecutionResult, MCPAgent
    from config.settings import EvalSettings

    settings = EvalSettings(
        model=model,  # type: ignore[arg-type]
        verbose=verbose,
        mcp_url=mcp_url,
    )

    agent = MCPAgent(settings)
    session_id: str | None = None
    conversation_history: list[ExecutionResult] = []

    print_welcome(model, mcp_url)

    while True:
        try:
            # Get input with support for multi-line
            lines = []
            prompt_char = ">" if not lines else "..."
            while True:
                try:
                    line = console.input(f"[bold blue]{prompt_char}[/bold blue] ")
                except EOFError:
                    if not lines:
                        raise
                    break

                if line.endswith("\\"):
                    lines.append(line[:-1])
                    prompt_char = "..."
                else:
                    lines.append(line)
                    break

            prompt = "\n".join(lines).strip()

        except (EOFError, KeyboardInterrupt):
            console.print("\n[dim]Goodbye![/dim]")
            break

        if not prompt:
            continue

        # Handle commands
        if prompt.startswith("/"):
            cmd = prompt.lower().split()[0]

            if cmd == "/quit" or cmd == "/exit":
                console.print("[dim]Goodbye![/dim]")
                break

            elif cmd == "/new":
                session_id = None
                conversation_history = []
                console.print("[green]Started new session[/green]")
                continue

            elif cmd == "/cost":
                if not conversation_history:
                    console.print("[yellow]No queries in session yet[/yellow]")
                else:
                    total_cost = sum(r.total_cost_usd or 0 for r in conversation_history)
                    total_input = sum(r.input_tokens for r in conversation_history)
                    total_output = sum(r.output_tokens for r in conversation_history)

                    table = Table(title="Session Costs")
                    table.add_column("Metric", style="cyan")
                    table.add_column("Value", style="green")

                    table.add_row("Queries", str(len(conversation_history)))
                    table.add_row("Total Cost", f"${total_cost:.6f}")
                    table.add_row("Input Tokens", f"{total_input:,}")
                    table.add_row("Output Tokens", f"{total_output:,}")
                    table.add_row("Avg Cost/Query", f"${total_cost / len(conversation_history):.6f}")

                    console.print(table)
                continue

            elif cmd == "/tools":
                if not conversation_history:
                    console.print("[yellow]No queries in session yet[/yellow]")
                else:
                    all_tools = []
                    for i, r in enumerate(conversation_history):
                        for tc in r.tool_calls:
                            all_tools.append((i + 1, tc.name, tc.duration_ms))

                    if not all_tools:
                        console.print("[yellow]No tool calls recorded[/yellow]")
                    else:
                        table = Table(title="Tool Calls")
                        table.add_column("Query", style="dim")
                        table.add_column("Tool", style="cyan")
                        table.add_column("Duration", style="green")

                        for query_num, tool_name, duration in all_tools:
                            table.add_row(
                                str(query_num),
                                tool_name,
                                f"{duration}ms",
                            )

                        console.print(table)
                continue

            elif cmd == "/clear":
                console.clear()
                continue

            elif cmd == "/verbose":
                verbose = not verbose
                settings.verbose = verbose
                console.print(f"[green]Verbose mode: {'on' if verbose else 'off'}[/green]")
                continue

            elif cmd == "/help":
                print_help()
                continue

            else:
                console.print(f"[red]Unknown command: {cmd}[/red]")
                console.print("[dim]Type /help for available commands[/dim]")
                continue

        # Execute the query
        console.print("[dim]Thinking...[/dim]")

        try:
            result = await agent.execute(prompt, session_id=session_id)
            conversation_history.append(result)

            # Update session_id for continuity
            if result.session_id:
                session_id = result.session_id

            console.print()
            print_result(result, verbose)
            console.print()

        except Exception as e:
            console.print(f"[red]Error: {e}[/red]")


def main() -> None:
    """Main entry point for REPL."""
    parser = argparse.ArgumentParser(
        description="Interactive REPL for ethpandaops-panda evaluation",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  uv run python -m scripts.repl
  uv run python -m scripts.repl --model claude-haiku-4-5
  uv run python -m scripts.repl --verbose --url http://localhost:2480
  uv run panda-repl --verbose
        """,
    )
    parser.add_argument(
        "--model",
        default="claude-sonnet-4-5",
        choices=["claude-sonnet-4-5", "claude-opus-4-5", "claude-haiku-4-5"],
        help="Claude model to use (default: claude-sonnet-4-5)",
    )
    parser.add_argument(
        "-v",
        "--verbose",
        action="store_true",
        help="Enable verbose output (show tokens, costs, etc.)",
    )
    parser.add_argument(
        "--url",
        default="http://localhost:2480",
        help="ethpandaops-panda server URL (default: http://localhost:2480)",
    )
    args = parser.parse_args()

    try:
        asyncio.run(repl_loop(args.model, args.verbose, args.url))
    except KeyboardInterrupt:
        console.print("\n[dim]Interrupted[/dim]")
        sys.exit(0)


if __name__ == "__main__":
    main()
