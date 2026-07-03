---
name: investigate
description: Debug Ethereum devnet or network issues. Use when diagnosing finality delays, network splits, offline nodes, client bugs, or general network health problems. Works for both local Kurtosis devnets and remote hosted deployments.
argument-hint: <network-name and/or issue description>
user-invocable: false
---

# Investigate Ethereum Network Issues

This skill resolves the target network, then hands off to the debugging runbook. One
runbook owns the procedure for every network kind — only the access layer differs.

**The user MUST specify which network to debug.** If not provided, ask them.

## Step 1: Resolve the network target

Run both checks in parallel:

```bash
# Check for local Kurtosis enclaves
kurtosis enclave ls 2>/dev/null

# Check for remote datasources and the local-kurtosis ClickHouse datasource via panda
panda datasources --json 2>/dev/null
```

- Target matches a **Kurtosis enclave name** → `kind: local-enclave`.
- Target is found in **panda datasources** (Dora networks, or container logs in
  `external.otel_logs` on `clickhouse-raw`) → `kind: hosted`.
- Found in **neither** → stop and tell the user the network was not found in any local
  enclave or remote datasource.

## Step 2: Follow the debugging runbook

```bash
panda read runbooks://debug_ethereum_network
```

Follow it with the resolved target and the user's symptom. It owns the symptom→branch
table, the CL-vs-EL localization matrix, and the per-kind access table (local OTel via
`local-kurtosis` vs hosted `external.otel_logs`, Kurtosis ports vs published
endpoints). Unsure which runbook applies at any point →
`panda search runbooks "<symptom>"`.

## Notes

- The runbooks are the source of truth for the debugging path — this skill only
  resolves the target and routes.
- Use `panda search examples` for query patterns before writing complex queries from
  scratch.
- Produce a debug report file at `/workspace/` and give the user the path at the end.
