# Re-proving changed code with many agents

From eth-act/ml-dsa-fv, 2026-11-02 to 04 (Lean 4.33). Gas-optimised EVM bytecode for ML-DSA-44/65/87 changed in about 15 subroutines (v3 → v4), and a second build used new opcodes from a future fork (Glamsterdam: EIP-8024 DUPN/SWAPN/EXCHANGE). Both were proved end to end: the same four final theorems per parameter set, only `propext`, `Classical.choice`, `Quot.sound`, no `native_decide`. About 1,100 modules, 108,000 constants.

## Order of work

1. **Freeze the bytes first.** The proof targets exact bytes; generator changes during the proof invalidate work. If a proof shows the code is wrong, stop and decide, don't patch silently.
2. **Keep the old proved bytes reproducible.** Gate every code change behind a generator version (`--version v3` still produces the proved bytes, SHA-pinned in the selftest). Move the old bytes to `NN/v3/`; the new version takes the canonical path.
3. **Retarget mechanically.** Regenerate the artifact modules from the new bytes, then relocate the proofs of unchanged code by an instruction-index and pc map from old to new bytes (`proofgen_port.py --reloc`; difflib-matched stretches inside changed regions, not only prefix and suffix). The kernel re-checks everything, so a wrong map fails loudly.
4. **State every contract before proving any.** The lead states the new subroutine contracts with `sorry` and lists them as open items (`#open_items` in an axioms check file) grouped into work packages. Segments and the composition can then be proved against stated contracts in parallel.
5. **Split by files, not by theorems.** Each package owns its files (one subroutine's contract, block files, helpers). Shared files (layout, budgets, gas schedule) are edited only by the lead or by agreement. Packages that depend on another's contract assume its statement, agreed early, and close when it lands.
6. **Contract changes need the lead's approval** and a one-line reason tied to the code change. Flag every weaker postcondition or stronger precondition, even when the final theorems are unaffected; write the list into the plan file. The gate reviewer needs it.
7. **Port proofs across parameter sets** with the same relocation tool; keep hand adjustments in a per-set port script the gate reruns.
8. **Gate on a big machine,** then merge by fast-forward so master holds exactly the gated commits.

## The proof finds bugs the tests don't

- The v4 ML-DSA-44 bytes ran out of gas instead of returning 0 on SampleInBall's fail path: a new schedule left flag words in a buffer the fail path didn't clear, and a later step read them as addresses ≥ 2^64. Selftests, fuzzing, a 3,639-case corpus, worst-case runs and three EVM implementations all missed it, because no input reaches that path in practice. The theorem quantifies over all calldata, so it could not be proved.
- Fix code without moving program counters when proofs already exist: keep the faulty exit's size and jump to a routine appended at the end, so relocation carries every finished proof over unchanged.
- Add a permanent test that forces the path (a debug build that rejects every byte), and check that it fails on the unfixed build.

## Extending the trust boundary

- New opcodes need the semantics the theorems are stated against. Upstream (powdr-labs/evm-semantics) had no fork with them, so a minimal patch was vendored (`deps/evm-semantics`, pinned as a git package of the repo itself, since the gate refuses path dependencies): a partial `Fork.Amsterdam` = Osaka plus exactly the two EIPs, +146/−80 lines.
- Prove the patch inert for existing forks (availability, gas, size limits equal to a verbatim copy of the original) and check new decoders against the reference implementation on every input (EELS tables over all 256 bytes).
- Add the new instructions to the stepper as a separate layer with its own soundness theorem, so no existing statement changes.
- State the new final theorems over the new fork in separate spec files that differ from the old ones only in the fork.
- Generated straight-line proofs for big unrolled kernels: co-simulate against a reference implementation to find block boundaries, symbolically execute for the terms, and prove them equal to the existing round model, so its lemmas are reused.

## Agents and machines

- A 4-core / 7.7 GB orb runs about two Lean processes at once; a full retarget lead on one became the bottleneck. Put packages on 16-core orbs (`a1.3xlarge`) and keep the lead for integration.
- A gate replay worker needs about 5 GB. On 4 cores both revisions take 4–5 h; on 16 cores with 8 workers, 15–40 min.
- Reuse finished package agents for the next round's packages: they know the code.
- The lead merges package branches as they land and tells every package the new base SHA after any byte change.
