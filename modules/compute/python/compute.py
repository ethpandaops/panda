"""Thin compute wrappers over server operations.

Compute is a control-plane for ephemeral compute sandboxes (Firecracker
microVMs). You create a sandbox from a template, it runs, and you can snapshot
it, stop/start/lease (extend its TTL) it, restore snapshots into fresh
sandboxes, and poll asynchronous operations.

Mutations (create/delete/stop/start/snapshot/restore) are asynchronous: they
return an operation object whose ``id`` you poll with :func:`get_operation`
until its ``status`` settles (e.g. ``succeeded``/``failed``).

The ``datasource`` parameter can be omitted when a single compute datasource is
configured.
"""

from __future__ import annotations

import os
from typing import Any

from ethpandaops import _runtime


def _require_compute_available() -> None:
    if not os.environ.get("ETHPANDAOPS_COMPUTE_DATASOURCES", "").strip():
        raise ValueError(
            "Compute is not enabled or no compute datasources are available."
        )


def _args(datasource: str | None, **kwargs: Any) -> dict[str, Any]:
    args: dict[str, Any] = {}
    if datasource is not None:
        args["datasource"] = datasource
    for key, value in kwargs.items():
        if value is not None:
            args[key] = value
    return args


def _page_args(
    datasource: str | None, limit: int, offset: int, cursor: str | None
) -> dict[str, Any]:
    args: dict[str, Any] = {"limit": limit, "offset": offset}
    if datasource is not None:
        args["datasource"] = datasource
    if cursor is not None:
        args["cursor"] = cursor
    return args


# --- Directory ---------------------------------------------------------------


def list_datasources() -> list[dict[str, Any]]:
    """List available compute datasources.

    Each entry has keys: name, description, url, type.
    """
    _require_compute_available()
    data = _runtime.invoke_data("compute.list_datasources")
    return data.get("datasources", [])


def list_users(datasource: str | None = None) -> Any:
    """List directory users."""
    _require_compute_available()
    return _runtime.invoke_json("compute.list_users", _args(datasource))


def get_user(handle: str, datasource: str | None = None) -> Any:
    """Get one directory user by handle."""
    _require_compute_available()
    return _runtime.invoke_json("compute.get_user", _args(datasource, handle=handle))


def list_nodes(datasource: str | None = None) -> Any:
    """List compute nodes backing the service."""
    _require_compute_available()
    return _runtime.invoke_json("compute.list_nodes", _args(datasource))


def get_node(node_id: str, datasource: str | None = None) -> Any:
    """Get one compute node by id."""
    _require_compute_available()
    return _runtime.invoke_json("compute.get_node", _args(datasource, id=node_id))


def list_audit(datasource: str | None = None) -> Any:
    """List audit-log entries."""
    _require_compute_available()
    return _runtime.invoke_json("compute.list_audit", _args(datasource))


def meta(datasource: str | None = None) -> Any:
    """Get service metadata (version, limits, capabilities)."""
    _require_compute_available()
    return _runtime.invoke_json("compute.meta", _args(datasource))


def auth_session(datasource: str | None = None) -> Any:
    """Get the authenticated session and identity (subject, handle, email)."""
    _require_compute_available()
    return _runtime.invoke_json("compute.auth_session", _args(datasource))


# --- Sandboxes ---------------------------------------------------------------


def list_sandboxes(
    datasource: str | None = None,
    limit: int = 100,
    offset: int = 0,
    cursor: str | None = None,
) -> Any:
    """List sandboxes (paginated)."""
    _require_compute_available()
    return _runtime.invoke_json(
        "compute.list_sandboxes", _page_args(datasource, limit, offset, cursor)
    )


def get_sandbox(sandbox_id: str, datasource: str | None = None) -> Any:
    """Get one sandbox by id."""
    _require_compute_available()
    return _runtime.invoke_json("compute.get_sandbox", _args(datasource, id=sandbox_id))


def create_sandbox(
    template: str,
    ttl: str | None = None,
    on_delete: str | None = None,
    idempotency_key: str | None = None,
    datasource: str | None = None,
) -> Any:
    """Create a sandbox from a template.

    ``ttl`` is a Go-duration string (e.g. ``"1h"``). ``on_delete`` is one of
    ``archive``, ``cold``, ``delete``, ``hot``. Returns an operation object;
    poll its ``id`` with :func:`get_operation`.
    """
    _require_compute_available()
    args = _args(
        datasource,
        template=template,
        ttl=ttl,
        on_delete=on_delete,
        idempotency_key=idempotency_key,
    )
    return _runtime.invoke_json("compute.create_sandbox", args)


def delete_sandbox(
    sandbox_id: str,
    idempotency_key: str | None = None,
    datasource: str | None = None,
) -> Any:
    """Delete a sandbox. Returns an operation to poll."""
    _require_compute_available()
    args = _args(datasource, id=sandbox_id, idempotency_key=idempotency_key)
    return _runtime.invoke_json("compute.delete_sandbox", args)


def stop_sandbox(
    sandbox_id: str,
    idempotency_key: str | None = None,
    datasource: str | None = None,
) -> Any:
    """Stop a running sandbox. Returns an operation to poll."""
    _require_compute_available()
    args = _args(datasource, id=sandbox_id, idempotency_key=idempotency_key)
    return _runtime.invoke_json("compute.stop_sandbox", args)


def start_sandbox(
    sandbox_id: str,
    idempotency_key: str | None = None,
    datasource: str | None = None,
) -> Any:
    """Start a stopped sandbox. Returns an operation to poll."""
    _require_compute_available()
    args = _args(datasource, id=sandbox_id, idempotency_key=idempotency_key)
    return _runtime.invoke_json("compute.start_sandbox", args)


def snapshot_sandbox(
    sandbox_id: str,
    note: str | None = None,
    ttl: str | None = None,
    idempotency_key: str | None = None,
    datasource: str | None = None,
) -> Any:
    """Snapshot a sandbox's current state. Returns an operation to poll.

    ttl is an optional Go-style duration for the snapshot's lifetime ("0" means
    no expiry; omit for the server default).
    """
    _require_compute_available()
    args = _args(
        datasource, id=sandbox_id, note=note, ttl=ttl, idempotency_key=idempotency_key
    )
    return _runtime.invoke_json("compute.snapshot_sandbox", args)


def lease_sandbox(
    sandbox_id: str,
    extend: str,
    datasource: str | None = None,
) -> Any:
    """Extend a sandbox's TTL.

    ``extend`` is a Go-duration string (e.g. ``"30m"``, ``"2h"``).
    """
    _require_compute_available()
    args = _args(datasource, id=sandbox_id, extend=extend)
    return _runtime.invoke_json("compute.lease_sandbox", args)


def prepare_sandbox_ssh(
    sandbox_id: str,
    public_key: str,
    datasource: str | None = None,
) -> Any:
    """Mint a short-lived SSH gateway certificate for a registered public key.

    Returns the gateway ``host``, ``port``, ``username``, and the
    ``client_certificate`` to present alongside the matching private key.
    The certificate expires within minutes; connect immediately.
    """
    _require_compute_available()
    args = _args(datasource, id=sandbox_id, public_key=public_key)
    return _runtime.invoke_json("compute.prepare_sandbox_ssh", args)


def get_sandbox_snapshots(sandbox_id: str, datasource: str | None = None) -> Any:
    """List snapshots taken from a sandbox."""
    _require_compute_available()
    return _runtime.invoke_json(
        "compute.get_sandbox_snapshots", _args(datasource, id=sandbox_id)
    )


def get_sandbox_operations(sandbox_id: str, datasource: str | None = None) -> Any:
    """List async operations run against a sandbox."""
    _require_compute_available()
    return _runtime.invoke_json(
        "compute.get_sandbox_operations", _args(datasource, id=sandbox_id)
    )


def get_sandbox_logs(sandbox_id: str, datasource: str | None = None) -> Any:
    """Fetch a sandbox's logs."""
    _require_compute_available()
    return _runtime.invoke_json(
        "compute.get_sandbox_logs", _args(datasource, id=sandbox_id)
    )


def get_sandbox_lineage(sandbox_id: str, datasource: str | None = None) -> Any:
    """Get a sandbox's lineage (snapshot/restore ancestry)."""
    _require_compute_available()
    return _runtime.invoke_json(
        "compute.get_sandbox_lineage", _args(datasource, id=sandbox_id)
    )


# --- Snapshots ---------------------------------------------------------------


def list_snapshots(
    datasource: str | None = None,
    limit: int = 100,
    offset: int = 0,
    cursor: str | None = None,
) -> Any:
    """List snapshots (paginated)."""
    _require_compute_available()
    return _runtime.invoke_json(
        "compute.list_snapshots", _page_args(datasource, limit, offset, cursor)
    )


def get_snapshot(snapshot_id: str, datasource: str | None = None) -> Any:
    """Get one snapshot by id."""
    _require_compute_available()
    return _runtime.invoke_json(
        "compute.get_snapshot", _args(datasource, id=snapshot_id)
    )


def delete_snapshot(
    snapshot_id: str,
    idempotency_key: str | None = None,
    datasource: str | None = None,
) -> Any:
    """Delete a snapshot. Returns an operation to poll."""
    _require_compute_available()
    args = _args(datasource, id=snapshot_id, idempotency_key=idempotency_key)
    return _runtime.invoke_json("compute.delete_snapshot", args)


def restore_snapshot(
    snapshot_id: str,
    ttl: str | None = None,
    idempotency_key: str | None = None,
    datasource: str | None = None,
) -> Any:
    """Restore a snapshot into a new sandbox.

    ``ttl`` is a Go-duration string (e.g. ``"1h"``). Returns an operation to
    poll for the new sandbox.
    """
    _require_compute_available()
    args = _args(datasource, id=snapshot_id, ttl=ttl, idempotency_key=idempotency_key)
    return _runtime.invoke_json("compute.restore_snapshot", args)


def get_snapshot_restored_by(snapshot_id: str, datasource: str | None = None) -> Any:
    """List sandboxes restored from a snapshot."""
    _require_compute_available()
    return _runtime.invoke_json(
        "compute.get_snapshot_restored_by", _args(datasource, id=snapshot_id)
    )


# --- Templates ---------------------------------------------------------------


def list_templates(
    datasource: str | None = None,
    limit: int = 100,
    offset: int = 0,
    cursor: str | None = None,
) -> Any:
    """List sandbox templates (paginated)."""
    _require_compute_available()
    return _runtime.invoke_json(
        "compute.list_templates", _page_args(datasource, limit, offset, cursor)
    )


def get_template(name: str, version: str, datasource: str | None = None) -> Any:
    """Get one template by name and version."""
    _require_compute_available()
    return _runtime.invoke_json(
        "compute.get_template", _args(datasource, name=name, version=version)
    )


# --- Operations --------------------------------------------------------------


def list_operations(
    datasource: str | None = None,
    limit: int = 100,
    offset: int = 0,
    cursor: str | None = None,
) -> Any:
    """List async operations (paginated)."""
    _require_compute_available()
    return _runtime.invoke_json(
        "compute.list_operations", _page_args(datasource, limit, offset, cursor)
    )


def get_operation(operation_id: str, datasource: str | None = None) -> Any:
    """Poll one async operation by id (the id returned by mutations)."""
    _require_compute_available()
    return _runtime.invoke_json(
        "compute.get_operation", _args(datasource, id=operation_id)
    )


# --- SSH keys ----------------------------------------------------------------


def list_ssh_keys(
    datasource: str | None = None,
    limit: int = 100,
    offset: int = 0,
    cursor: str | None = None,
) -> Any:
    """List the caller's SSH public keys."""
    _require_compute_available()
    return _runtime.invoke_json(
        "compute.list_ssh_keys", _page_args(datasource, limit, offset, cursor)
    )


def add_ssh_key(
    public_key: str,
    name: str | None = None,
    datasource: str | None = None,
) -> Any:
    """Register an SSH public key for the caller."""
    _require_compute_available()
    args = _args(datasource, public_key=public_key, name=name)
    return _runtime.invoke_json("compute.add_ssh_key", args)


def delete_ssh_key(key_id: str, datasource: str | None = None) -> Any:
    """Delete one of the caller's SSH public keys."""
    _require_compute_available()
    return _runtime.invoke_json("compute.delete_ssh_key", _args(datasource, id=key_id))
