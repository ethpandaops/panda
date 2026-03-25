"""Consensus-specs access: protocol constants and spec documents.

Example usage:
    from ethpandaops import specs

    # Get a constant (latest fork that defines it)
    value = specs.get_constant("MAX_EFFECTIVE_BALANCE")
    print(value)  # {'name': 'MAX_EFFECTIVE_BALANCE', 'value': '32000000000', 'fork': 'phase0'}

    # Get a constant for a specific fork
    value = specs.get_constant("MAX_EFFECTIVE_BALANCE", fork="phase0")

    # List constants matching a prefix
    constants = specs.list_constants(prefix="MAX_")

    # List constants for a specific fork
    constants = specs.list_constants(fork="deneb")

    # Get a spec document
    spec = specs.get_spec("deneb", "beacon-chain")
    print(spec['title'])
"""

from __future__ import annotations

from typing import Any

from ethpandaops._runtime import invoke_data


def get_constant(name: str, fork: str | None = None) -> dict[str, Any]:
    """Get a consensus-specs protocol constant by name.

    Args:
        name: Constant name (e.g., "MAX_EFFECTIVE_BALANCE"). Case-insensitive.
        fork: Optional fork filter (e.g., "deneb"). When omitted, returns the
              constant from the latest fork that defines it.

    Returns:
        Dict with 'name', 'value', and 'fork' keys.
    """
    args: dict[str, Any] = {"name": name}
    if fork:
        args["fork"] = fork

    return invoke_data("specs.get_constant", args)


def list_constants(
    fork: str | None = None, prefix: str | None = None
) -> list[dict[str, str]]:
    """List consensus-specs protocol constants.

    Args:
        fork: Optional fork filter (e.g., "phase0", "deneb").
        prefix: Optional name prefix filter (e.g., "MAX_"). Case-insensitive.

    Returns:
        List of dicts, each with 'name', 'value', and 'fork' keys.
    """
    args: dict[str, Any] = {}
    if fork:
        args["fork"] = fork
    if prefix:
        args["prefix"] = prefix

    result = invoke_data("specs.list_constants", args)

    return result.get("constants", [])


def get_spec(fork: str, topic: str) -> dict[str, Any]:
    """Get a consensus spec document.

    Args:
        fork: Fork name (e.g., "deneb", "electra").
        topic: Topic name (e.g., "beacon-chain", "p2p-interface").

    Returns:
        Dict with 'fork', 'topic', 'title', 'content', and 'url' keys.
    """
    return invoke_data("specs.get_spec", {"fork": fork, "topic": topic})
