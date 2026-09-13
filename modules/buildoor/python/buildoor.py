"""Thin buildoor wrappers over server operations.

Drives devnet buildoor builder instances: per-slot action plans, jq payload/
bid/envelope transforms, and slot outcome history. Reads are open. Mutations
are credentialed by the panda proxy when it advertises buildoor; an explicit
authenticatoor bearer token instead goes direct and keeps per-user attribution
in buildoor's audit log. Plans freeze ~1 slot ahead — target slots at least 2
ahead of current.
"""

from __future__ import annotations

import os
from typing import Any

from ethpandaops import _runtime


def _require_buildoor_available() -> None:
    if not os.environ.get("ETHPANDAOPS_BUILDOOR_NETWORKS", "").strip():
        raise ValueError("Buildoor is not enabled or no buildoor deployments are available.")


def list_networks() -> list[dict[str, Any]]:
    """List networks with buildoor deployments.

    Each entry has keys: name, description, url, type, extra.
    """
    _require_buildoor_available()
    data = _runtime.invoke_data("buildoor.list_networks")
    return data.get("networks", [])


def list_instances(network: str) -> list[dict[str, Any]]:
    """List a network's builder instances. Each entry has keys: name, url."""
    _require_buildoor_available()
    data = _runtime.invoke_data("buildoor.list_instances", {"network": network})
    return data.get("instances", [])


def get_overview(network: str, instance: str) -> dict[str, Any]:
    """Instance status: current_slot, running, service states, builder pubkey."""
    _require_buildoor_available()
    payload = _runtime.invoke_json(
        "buildoor.get_overview", {"network": network, "instance": instance}
    )
    return payload if isinstance(payload, dict) else {}


def get_action_plan(
    network: str, instance: str, min_slot: int, max_slot: int
) -> dict[str, Any]:
    """Per-slot action plans in the inclusive range: {plans, min_slot, max_slot}."""
    _require_buildoor_available()
    payload = _runtime.invoke_json(
        "buildoor.get_action_plan",
        {"network": network, "instance": instance, "min_slot": min_slot, "max_slot": max_slot},
    )
    return payload if isinstance(payload, dict) else {}


def get_slot_results(
    network: str, instance: str, min_slot: int, max_slot: int
) -> dict[str, Any]:
    """Attempt-level outcome history per slot: build, bids, block submissions,
    reveal attempts, inclusion, and the frozen applied_plan."""
    _require_buildoor_available()
    payload = _runtime.invoke_json(
        "buildoor.get_slot_results",
        {"network": network, "instance": instance, "min_slot": min_slot, "max_slot": max_slot},
    )
    return payload if isinstance(payload, dict) else {}


def test_transform(
    network: str,
    instance: str,
    target: str,
    expression: str,
    sample_slot: int | None = None,
) -> dict[str, Any]:
    """Evaluate a jq expression against a sample builder object without
    touching any plan. target is payload, bid, or envelope; the input is the
    slot's captured artifact when sample_slot is given and available, else a
    template. Returns {target, input, input_source, output, error}."""
    _require_buildoor_available()
    args: dict[str, Any] = {
        "network": network,
        "instance": instance,
        "target": target,
        "expression": expression,
    }
    if sample_slot:
        args["sample_slot"] = sample_slot

    payload = _runtime.invoke_json("buildoor.test_transform", args)
    return payload if isinstance(payload, dict) else {}


def update_action_plan(
    network: str, instance: str, updates: list[dict[str, Any]], token: str | None = None
) -> dict[str, Any]:
    """Apply raw PlanUpdate mutations atomically. Buildoor owns the schema:
    each update targets slots and/or from_slot..to_slot with category members
    and fine-grained set paths (e.g. {"set": {"bid.bid_value_gwei": 5000}}).

    Credentials: without a token the mutation routes through a panda proxy
    that advertises buildoor (the proxy mints the devnet credential). Passing
    a personal authenticatoor bearer token goes direct instead and keeps
    per-user attribution in buildoor's audit log. Returns the authoritative
    {status, slots, plans}."""
    _require_buildoor_available()
    args: dict[str, Any] = {"network": network, "instance": instance, "updates": updates}
    if token:
        args["auth_token"] = token

    payload = _runtime.invoke_json("buildoor.update_action_plan", args)
    return payload if isinstance(payload, dict) else {}


def set_transforms(
    network: str,
    instance: str,
    token: str | None = None,
    slots: list[int] | None = None,
    from_slot: int | None = None,
    to_slot: int | None = None,
    payload: str | None = None,
    bid: str | None = None,
    envelope: str | None = None,
) -> dict[str, Any]:
    """Set jq transforms on future slots (>=2 ahead of current — plans freeze
    ~1 slot ahead). None leaves a transform unchanged; '' clears that one
    expression. Target via slots and/or the inclusive from_slot..to_slot range."""
    update: dict[str, Any] = {}
    if slots:
        update["slots"] = slots
    if from_slot is not None or to_slot is not None:
        if from_slot is None or to_slot is None:
            raise ValueError("from_slot and to_slot must be provided together.")
        update["from_slot"] = from_slot
        update["to_slot"] = to_slot
    if not update:
        raise ValueError("Provide target slots via slots or from_slot/to_slot.")

    expressions = {
        "transforms.payload": payload,
        "transforms.bid": bid,
        "transforms.envelope": envelope,
    }
    update["set"] = {key: expr for key, expr in expressions.items() if expr is not None}
    if not update["set"]:
        raise ValueError("Provide at least one of payload, bid, or envelope.")

    return update_action_plan(network, instance, [update], token)
