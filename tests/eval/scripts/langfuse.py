#!/usr/bin/env python
"""Langfuse management script for ethpandaops-panda evaluation."""

from __future__ import annotations

import argparse
import subprocess
import sys
from pathlib import Path


def get_compose_file() -> Path:
    """Get path to docker-compose.langfuse.yaml."""
    script_dir = Path(__file__).parent
    compose_file = script_dir.parent / "docker-compose.langfuse.yaml"
    if not compose_file.exists():
        print(f"Error: {compose_file} not found", file=sys.stderr)
        sys.exit(1)
    return compose_file


def run_compose(args: list[str]) -> int:
    """Run docker compose with given arguments."""
    compose_file = get_compose_file()
    cmd = ["docker", "compose", "-f", str(compose_file), *args]
    result = subprocess.run(cmd, cwd=compose_file.parent)
    return result.returncode


def cmd_up(args: argparse.Namespace) -> int:
    """Start Langfuse services."""
    print("Starting Langfuse...")
    return_code = run_compose(["up", "-d"])
    if return_code == 0:
        print()
        print("Langfuse is starting up. This may take 1-2 minutes on first run.")
        print()
        print("Pre-configured with default keys - no setup needed!")
        print()
        print("  UI:       http://localhost:3000")
        print("  Login:    admin@mcp.local / adminadmin")
        print("  Project:  mcp-eval")
        print()
        print("To enable tracing, add to your .env:")
        print("  MCP_EVAL_LANGFUSE_ENABLED=true")
        print()
        print("Commands:")
        print("  uv run python -m scripts.langfuse logs -f  # View logs")
        print("  uv run python -m scripts.langfuse down     # Stop")
    return return_code


def cmd_down(args: argparse.Namespace) -> int:
    """Stop Langfuse services."""
    print("Stopping Langfuse...")
    return run_compose(["down"])


def cmd_logs(args: argparse.Namespace) -> int:
    """View Langfuse logs."""
    compose_args = ["logs"]
    if args.follow:
        compose_args.append("-f")
    if args.service:
        compose_args.append(args.service)
    return run_compose(compose_args)


def cmd_status(args: argparse.Namespace) -> int:
    """Show Langfuse service status."""
    return run_compose(["ps"])


def cmd_reset(args: argparse.Namespace) -> int:
    """Reset Langfuse (delete all data)."""
    print("WARNING: This will delete all Langfuse data!")
    response = input("Are you sure? (yes/no): ")
    if response.lower() != "yes":
        print("Aborted.")
        return 1

    print("Stopping services...")
    run_compose(["down"])

    print("Removing volumes...")
    run_compose(["down", "-v"])

    print("Done. Run 'uv run python -m scripts.langfuse up' to start fresh.")
    return 0


def main() -> int:
    """Main entry point."""
    parser = argparse.ArgumentParser(
        description="Manage Langfuse for ethpandaops-panda evaluation",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  uv run python -m scripts.langfuse up      Start Langfuse
  uv run python -m scripts.langfuse down    Stop Langfuse
  uv run python -m scripts.langfuse logs -f View logs (follow mode)
  uv run python -m scripts.langfuse status  Check service status
  uv run python -m scripts.langfuse reset   Delete all data and reset
""",
    )

    subparsers = parser.add_subparsers(dest="command", required=True)

    # up command
    subparsers.add_parser("up", help="Start Langfuse services")

    # down command
    subparsers.add_parser("down", help="Stop Langfuse services")

    # logs command
    logs_parser = subparsers.add_parser("logs", help="View Langfuse logs")
    logs_parser.add_argument("-f", "--follow", action="store_true", help="Follow log output")
    logs_parser.add_argument("service", nargs="?", help="Specific service to view logs for")

    # status command
    subparsers.add_parser("status", help="Show service status")

    # reset command
    subparsers.add_parser("reset", help="Reset Langfuse (delete all data)")

    args = parser.parse_args()

    commands = {
        "up": cmd_up,
        "down": cmd_down,
        "logs": cmd_logs,
        "status": cmd_status,
        "reset": cmd_reset,
    }

    return commands[args.command](args)


if __name__ == "__main__":
    sys.exit(main())
