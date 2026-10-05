# Funding and recovery

Discover current network IDs with `panda ethnode networks`. Use `lb` for the
network's load balanced execution endpoint. Unlocked RPC accounts are not an
assumed funding path.

Save the wallet locally **before** starting a claim. For example:

```bash
umask 077
panda execute --code 'import json; from ethpandaops import evm; print(json.dumps(evm.wallet()))' > wallet.json
```

Read the address from that file and use it in a separate funding execution.
Keep the private key private; the faucet only needs the address.

Mining runs in the local panda-server. Execution defaults to 60
seconds; CLI `panda execute --timeout 600 --file fund.py` and MCP `timeout: 600`
extend that budget to the supported maximum. The server's claim budget does
not override the caller's execution or HTTP timeouts.

`evm.faucet` returns a submitted transaction hash, which can still be pending.
Check `eth_getTransactionReceipt` and the original address's `eth_getBalance`
through `ethnode.execution_rpc(network, "lb", ...)` before spending or retrying.
After a timeout or disconnect, do those checks first. A concurrent-session
error only proves a session exists, not successful funding.
Do not create a replacement wallet to recover a claim: funding targets the
original address.

## Long claims

Prefer `evm.faucet_start(network, address, amount_wei=100 * 10**18)` in one
execution and `evm.faucet_status(job_id)` in subsequent executions. Save the
job ID immediately. Start returns without mining in the execution process.
The server owns a 12-minute job budget, independent of execution cancellation
and HTTP disconnects. Server shutdown cancels work; job records are in memory
and do not survive a server restart.

States are `mining`, `submitted`, `confirmed`, and `failed`. Submission is an
attempt, not evidence of funds landing; a hash is provided when available.
`confirmed` requires a successful on-chain receipt. `terminal` means background
work has stopped. A terminal `submitted` job has an uncertain outcome; status
polls can recover its delayed hash/receipt. Check the original balance as well.

Repeating the same network, address and amount recovers the existing job for
one hour after background work ends. A stable optional `request_id` also
recovers that request. Reusing a request ID with another amount is rejected.
Use a **new** request ID only for an intentional additional claim, after
checking the previous claim's outcome. Records expire after one hour; after
expiry or restart, check balance/receipt before starting again.

The local server bounds work to eight active jobs, 256 retained jobs and 1,024
recovery keys. These resource limits supplement the faucet/proxy abuse limits;
they do not replace address quotas or wallet ceilings.
