---
name: Investigate Hive Test Failures
description: Explain why execution clients fail EELS tests in hive CI (hive.ethpandaops.io) — read a group's per-client pass and fail counts (hive.get_summary for glamsterdam-quick, heze, bogota, mainnet), what changed since the previous run (hive.get_diff regressions and fixes), a client's failing tests by EIP (hive.list_tests), which failures several clients share, and one test's error, logs, fill, consume and hive reproduce commands and hive_url (hive.get_test, hive.find_test). Use to decide whether a failure is a test or spec problem or a client bug.
tags: [hive, eels, execution, clients, regressions, tests]
triggers:
  - which hive tests is reth failing
  - why is this hive test failing for a client
  - hive regressions since the last run
  - is this hive failure a test bug or a client bug
  - eels consume-engine failures for glamsterdam
---

Owns reading hive CI results for execution clients: from a group's summary down to
one failing test's evidence, and the call between a test or spec problem and a
client bug. Live devnet behaviour belongs to `runbooks://debug_ethereum_network`;
how to cite evidence to `runbooks://evidence_discipline`.

## Inputs
Required: what to look at — a client, an EIP, a test name, or a whole group.
Preferred: the group. Groups are named after forks and devnets (`glamsterdam-quick`,
`heze`, `bogota`, `mainnet`); `-quick` groups run a subset of tests more often. With
no group given, list the groups and pick the one for the fork in question.

## Output
Findings grouped by class (see the table below), each an evidence item per
`runbooks://evidence_discipline` with the test's `hive_url` as its reference.
Illustrative:

```yaml
- source: hive
  ref: "https://hive.ethpandaops.io/#/test/glamsterdam-quick/1793911066-f818a2221da12112fa8be06687460bb9?testnumber=4754"
  at: "2026-11-06T06:38Z run"
  detail: "reth and go-ethereum fail test_bal_withdrawals_and_dequeues_net_balance_at_last_index; besu, nethermind pass"
```

## Procedure

Python below is a shape to adapt — substitute the group, client and pattern.

1. **Read the group's state and when it ran.** Results lag CI by hours and a group
   mixes runs from different times; quote `last_run` with every claim.

   ```python
   from ethpandaops import hive
   summary = hive.get_summary("glamsterdam-quick")
   summary["last_run"], {c: (v["fail"], v["version"], v["branch"]) for c, v in summary["clients"].items()}
   ```

2. **See what changed.** New failures since the previous batch of runs are usually
   the question; unchanged failures are known.

   ```python
   diff = hive.get_diff("glamsterdam-quick")          # totals only; include_tests=True lists them
   {c: e["total"] for c, e in diff["regressions"].items()}
   ```

3. **List the client's failing tests, grouped by EIP.** Test modules sit under the
   fork and EIP directory (`tests/amsterdam/eip7928_.../`); `filter` is a
   case-insensitive regexp on the pytest ID.

   ```python
   failing = hive.list_tests("glamsterdam-quick", client="reth", filter="eip7928", limit=0)
   failing["total"], [t["pytest_id"] for t in failing["tests"][:10]]
   ```

4. **Check which clients share each failure** before blaming one: list the group's
   failing tests without a client and count clients per test function.

   ```python
   from collections import defaultdict
   clients = defaultdict(set)
   for t in hive.list_tests("glamsterdam-quick", limit=0)["tests"]:
       clients[t["function"]].add(t["client"])
   ```

5. **Read one test's evidence.** `get_test` takes the `suite_file` and `test_id` of a
   listed test; `find_test` takes the first match of a pattern instead.

   ```python
   detail = hive.find_test("glamsterdam-quick", "test_bal_withdrawals_and_dequeues", client="reth")
   detail["error"], detail["detail_log"], detail["client_log"]   # client log is the last 30 lines
   detail["fill_command"], detail["consume_command"], detail["hive_command"], detail["hive_url"]
   ```

## Classify each failure

| Evidence | Class | Next |
|---|---|---|
| One client fails, the others pass | client bug | the client's logs; the fill and consume commands reproduce it locally |
| Several clients fail the same new test, others pass | test or spec problem, or a shared misreading of the spec | the test source (`source_url`) and the EIP text before any client |
| Every client fails | fixtures, simulator or spec problem | the run's `fixtures_release` (hive.get_failures) and the test source |
| A client fails nearly everything in a run | the client or its image did not start or does not support the fork | the client log's first lines; its `branch` and `version` in the summary |

## Self-Check
- Every finding names the group, the run time and a `hive_url`.
- A test-or-spec verdict lists which clients fail and which pass.
- No claim relies on a group whose `last_run` predates the change it is about.
