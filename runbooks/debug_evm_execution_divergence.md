---
name: Debug EVM Execution Divergence
description: Judge whether Ethereum execution clients disagree on EVM execution — run the same bytecode or transaction against every client with evm.trace / evm.trace_tx (debug_traceCall structLogs), align the traces, locate the first diverging opcode or gas cost, and classify the divergence as a client bug, spec ambiguity, or fork-activation difference. Use for new-opcode trials (EIP-8024 DUPN/SWAPN/EXCHANGE), gas cost mismatches, evm-fuzz triage, and any "clients return different results for the same input".
tags: [evm, execution, divergence, opcodes, tracing, gas]
triggers:
  - same bytecode different gas or result across clients
  - clients disagree on evm execution locate the diverging opcode
  - trace a new opcode like dupn swapn exchange across clients
  - triage a diverging evm fuzz transaction
  - is this evm behavior a client bug or spec ambiguity
prerequisites: [ethnode]
---

Owns the JUDGMENT of cross-client EVM divergence: tracing one input on every client,
finding the first diverging step, and classifying it. The `evm` library owns the
mechanics (`assemble`, `disassemble`, `call`, `trace`, `trace_tx` — look up exact
call patterns by searching the examples index for "evm opcode trace"); escalation to
spec text belongs to `runbooks://ethereum_spec_source_drilldown`, and a confirmed
divergence enters the issue pipeline as an issue record
(`runbooks://devnet_issue_contract`, category `execution-mismatch`).

## Inputs
Required: a network with ≥2 EL client types, and the input under test — raw bytecode,
assembly to build with `evm.assemble`, or a transaction hash to replay.
Preferred: the client/instance list and the suspected opcode or gas area.

## Output
A divergence verdict: the first diverging step (opcode, pc, gas cost, stack effect),
per-client behavior at that step, the classification, and evidence — every claim
carrying the exact trace call that re-derives it (`runbooks://evidence_discipline`).

## Procedure

Python is a shape to adapt — substitute network, instances, and input.

1. **Resolve the client set.** One instance per EL client family, identified by role +
   client + image (`runbooks://debug_ethereum_network` owns target resolution).
   Execution runs on the target devnet node, so fork rules are always the network's
   own — no local EVM simulation drift.
2. **Trace the same input on every client:**

   ```python
   from ethpandaops import evm
   code = evm.assemble(["PUSH1", 1, "PUSH1", 2, "ADD", "STOP"])
   traces = {i: evm.trace("<network>", i, code) for i in instances}   # structLogs per client
   # for an on-chain tx: evm.trace_tx("<network>", i, tx_hash)
   ```

3. **Align step-by-step and find the FIRST divergence.** Compare `(op, pc, gasCost,
   stack depth/top)` per step across clients; the first differing step is the
   divergence point — later differences (final gas, return data, revert) are usually
   consequences. Judge at the first diverging step, not the final result.
4. **Classify:**

   | Pattern | Class |
   | --- | --- |
   | One client differs, others agree | client-bug candidate — that client at that opcode |
   | Clients split into groups | spec ambiguity or fork-activation difference — check each client's active fork rules first (`runbooks://ethereum_protocol_model`) |
   | All agree but result surprises | expectation bug — re-read the spec, not the clients |
   | Divergence only under specific stack/memory state | edge case — minimize the reproducer by shrinking the bytecode |

5. **Escalate and file.** For a client-bug or spec-ambiguity verdict, resolve the
   spec rule and the exact client commit via
   `runbooks://ethereum_spec_source_drilldown`, then emit an issue record with the
   minimized reproducer bytecode in the reproduction recipe and the trace calls as
   evidence — `handles` stays reproducibility ids + setup summary
   (`runbooks://devnet_issue_contract`).

## Rules

- **Minority is not wrong.** A 3-vs-1 split identifies where to look, not who is
  buggy — the majority can share the bug (`runbooks://evidence_discipline`).
- **Minimize before filing.** Shrink the bytecode to the smallest input that still
  diverges; a one-opcode reproducer is the difference between a report and a fix.
- **Pin the fork context.** An opcode gated by a fork (e.g. EIP-8024 ops) diverges
  "correctly" when clients disagree on activation — verify the active fork at the
  trace block before calling it an EVM bug.

## Self-Check

Before returning:
- The same exact input was traced on every client in the set.
- The verdict names the first diverging step, not a downstream difference.
- Fork activation at the trace block was checked before blaming an implementation.
- A filed divergence carries the minimized reproducer and re-derivable trace calls.
