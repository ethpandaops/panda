"""Thin hive wrappers over server operations.

Hive results are the EELS consume runs ethPandaOps CI publishes at
https://hive.ethpandaops.io, grouped by fork or devnet (for example
glamsterdam-quick or heze). Each group holds the latest run of every client
with every simulator. Every test carries a hive_url, its page in the hive UI
with the full description, error and client logs.
"""

from __future__ import annotations

from typing import Any

from ethpandaops import _runtime


def _args(**kwargs: Any) -> dict[str, Any]:
    return {key: value for key, value in kwargs.items() if value is not None}


def list_groups() -> list[dict[str, Any]]:
    """List result groups. Each entry has keys: name, type, url."""
    data = _runtime.invoke_data("hive.list_groups")
    return data.get("groups", [])


def get_summary(group: str) -> dict[str, Any]:
    """Pass, fail and timeout counts per client and simulator.

    Keys: group, last_run, clients (version, repo, branch, commit, pass,
    fail, timeout per client) and simulators (pass, fail, total).
    """
    return _runtime.invoke_data("hive.get_summary", {"group": group})


def get_failures(
    group: str, client: str | None = None, simulator: str | None = None
) -> dict[str, Any]:
    """Failures and fail rates per client and simulator.

    Each simulator entry has the run_date, suite_file and fixtures_release
    of the latest run, with total_fails, total_tests and fail_rate.
    """
    return _runtime.invoke_data(
        "hive.get_failures", _args(group=group, client=client, simulator=simulator)
    )


def list_tests(
    group: str,
    status: str = "fail",
    client: str | None = None,
    simulator: str | None = None,
    filter: str | None = None,
    offset: int = 0,
    limit: int = 50,
) -> dict[str, Any]:
    """List tests of the latest runs.

    status is fail, pass or all. filter is a case-insensitive regexp on the
    pytest ID. Returns total and tests, each with client, simulator, module,
    function, pytest_id, fork, pass, suite_file, test_id, source_url and
    hive_url.
    """
    return _runtime.invoke_data(
        "hive.list_tests",
        _args(
            group=group,
            status=status,
            client=client,
            simulator=simulator,
            filter=filter,
            offset=offset,
            limit=limit,
        ),
    )


def search(
    group: str,
    query: str,
    client: str | None = None,
    simulator: str | None = None,
    status: str | None = None,
    limit: int = 50,
) -> dict[str, Any]:
    """Tests whose pytest ID contains query, ignoring case."""
    return _runtime.invoke_data(
        "hive.search",
        _args(
            group=group,
            query=query,
            client=client,
            simulator=simulator,
            status=status,
            limit=limit,
        ),
    )


def get_test(
    group: str, suite_file: str, test_id: str, full_log: bool = False
) -> dict[str, Any]:
    """One test's error, detail log, client log and reproduce commands.

    The client log is its last 30 lines unless full_log. Also has
    fill_command, consume_command and hive_command.
    """
    return _runtime.invoke_data(
        "hive.get_test",
        {
            "group": group,
            "suite_file": suite_file,
            "test_id": str(test_id),
            "full_log": full_log,
        },
    )


def find_test(
    group: str,
    filter: str,
    client: str | None = None,
    simulator: str | None = None,
    full_log: bool = False,
) -> dict[str, Any]:
    """get_test for the first test whose pytest ID matches filter."""
    return _runtime.invoke_data(
        "hive.find_test",
        _args(
            group=group,
            filter=filter,
            client=client,
            simulator=simulator,
            full_log=full_log,
        ),
    )


def get_diff(group: str, include_tests: bool = False) -> dict[str, Any]:
    """Regressions, fixes and unchanged failures between the latest two runs.

    Each section maps client to total and simulators. Test lists are left
    out unless include_tests, since a group with many failures can list
    tens of thousands of tests.
    """
    return _runtime.invoke_data(
        "hive.get_diff", {"group": group, "include_tests": include_tests}
    )


def link_test(group: str, suite_file: str, test_id: str) -> str:
    """Link to a test's page in the hive UI."""
    data = _runtime.invoke_data(
        "hive.link_test",
        {"group": group, "suite_file": suite_file, "test_id": str(test_id)},
    )
    return data.get("url", "")
