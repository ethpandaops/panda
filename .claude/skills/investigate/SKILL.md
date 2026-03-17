---
name: investigate
description: Debug Ethereum devnet or network issues. Use when diagnosing finality delays, network splits, offline nodes, client bugs, or general network health problems. Works for both local Kurtosis devnets and remote hosted deployments.
argument-hint: <network-name and/or issue description>
user-invocable: false
---

# Investigate Ethereum Network Issues

This skill routes to the appropriate debugging runbook based on whether the target is a local Kurtosis devnet or a remote hosted deployment.

**The user MUST specify which network to debug.** If not provided, ask them.

## Step 1: Detect local vs remote

Run both checks in parallel:

```bash
# Check for local Kurtosis enclaves
kurtosis enclave ls 2>/dev/null

# Check for remote datasources via panda
panda datasources --json 2>/dev/null
```

## Step 2: Route to the right runbook

- If the target network matches a **Kurtosis enclave name** → **local devnet**. Load the local debugging procedure:
  ```bash
  panda search runbooks "debug local devnet"
  ```
  Then follow that runbook. It covers Kurtosis service discovery, local Dora/Loki detection (localhost:3100), direct CL/EL API queries, Kurtosis-specific Loki label schema, and `kurtosis service logs` fallback.

- If the target network is found in **panda datasources** (Dora networks, Loki instances) → **remote deployment**. Load the remote debugging procedure:
  ```bash
  panda search runbooks "debug devnet"
  ```
  Then follow that runbook. It covers remote datasource discovery, Dora data collection, remote Loki log investigation, and ethnode RPC validation.

- If found in **neither** → stop, tell the user the network was not found in any local enclave or remote datasource.

## Notes

- The runbooks are the source of truth for each debugging path — this skill only routes to them.
- Both runbooks use `panda search examples` for query patterns — search before writing complex queries from scratch.
- Both runbooks produce a debug report file at `/workspace/` — provide the path to the user at the end.