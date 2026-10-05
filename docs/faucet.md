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

Synchronous mining runs in the local panda-server. Execution defaults to 60
seconds; CLI `panda execute --timeout 600 --file fund.py` and MCP `timeout: 600`
extend that budget to the supported maximum. The server's claim budget does
not override the caller's execution or HTTP timeouts.

`evm.faucet` returns a submitted transaction hash, which can still be pending.
Check `eth_getTransactionReceipt` and the original address's `eth_getBalance`
through `ethnode.execution_rpc(network, "lb", ...)` before spending or retrying.
After a timeout or disconnect, do those checks first. Mining continuation is
not guaranteed, and a concurrent-session error only proves a session exists.
Do not create a replacement wallet to recover a claim: funding targets the
original address.
