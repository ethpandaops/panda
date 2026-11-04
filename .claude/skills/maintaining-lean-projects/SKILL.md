---
name: maintaining-lean-projects
description: "Rules and workflows for large Lean 4 / Lake projects: keeping verification tooling sound (axiom audits, statement diffs, proof-result caches, gates, agent check loops), making builds and proofs faster (profiling slow modules, shared tactics, proof by reflection, import graph, scheduling, caches), cutting the gas of verified EVM bytecode, and re-proving changed code with many agents. Use when changing anything that decides what counts as proved, when speeding up a Lean build or proof, when optimizing the gas of bytecode a Lean project verifies, when retargeting proofs to new code, or when setting up CI or agent loops for a Lean repo."
---

# Maintaining Lean projects

Lessons from making eth-act/clifv (about 5,000 Lean 4.34 modules, a verified compiler backend) build faster, from an automated review of the turbo-lean changes made for it, from eth-act/ml-dsa-fv (about 1,100 Lean 4.33 modules proving EVM bytecode for ML-DSA): halving its build, gating every merge, cutting the verifier's gas, and re-proving two new versions of the bytecode, and from a proof-speed PR to a Lean 4.35 consensus formalization (about 450 modules). They apply to any large Lean repository.

Four references, loaded as needed:
- `reference/verification-tooling.md`: fourteen rules for tooling that decides what counts as proved, each with the bug it came from. **Read it before changing such tooling.**
- `reference/build-speed.md`: the build and proof-speed playbook, with measured effects and what didn't work, including symbolic-execution proofs (chunking, reflection, dischargers).
- `reference/proof-campaigns.md`: re-proving changed code: freezing bytes, mechanical retargeting, contracts first, splitting work across agents, extending the trust boundary.
- `reference/evm-gas.md`: optimizing the gas of verified EVM bytecode for cryptography: profiling, floors, techniques that paid, what was ruled out, validation.

## Which reference applies

| The change touches | Read |
|---|---|
| axiom audit, `#print axioms` parsing, allowlists, witness markers | verification-tooling |
| statement or signature diffs, sorry/axiom gates, CI gate jobs | verification-tooling |
| any cache of build outputs or proof verdicts, dedup keys | both |
| agent edit loops (language server, turbo-lean, MCP) | both |
| slow modules, tactics, `simp` usage, heartbeats | build-speed |
| imports, module splits, scheduler, CI sharding | build-speed |
| symbolic execution, `simp` over large terms, kernel time on generated proofs | build-speed |
| proving a new version of verified code, porting proofs, new opcodes or semantics | proof-campaigns, then verification-tooling |
| splitting proof work across agents or orbs | proof-campaigns |
| gas of verified EVM bytecode, bytecode generators, gas floors, new EIPs | evm-gas, then proof-campaigns |

## Workflow for a change to verification tooling

1. Check the change against each rule in `reference/verification-tooling.md` and name, for each, where the change complies or why it doesn't apply.
2. Get an independent review: a reviewing subagent given the diff and the rules, told to look for ways the tool could report "proved" for something that isn't.
3. Fix each finding with a regression test that fails without the fix. Then review again, the fixes included, until a round comes back clean.
4. Test the gate with compiled mutations, not only hand-made inputs to its comparison step (rule 12).
5. In the PR, say which rules applied and how they are met.

## Workflow for a speed change

1. Measure first: the same machine and session, the module run alone, wall seconds and peak memory. Use heartbeats if the machine is shared, and check the tactic doesn't reset them.
2. Profile (`set_option trace.profiler true`, leanprof) and name the cause before changing anything.
3. Prefer changing a shared tactic over editing individual proofs.
4. Gate the result: theorem statements and axioms unchanged (signature diff over all constants, axiom audit), the importer cone built, and compiler or program output byte-identical where it applies.
5. Record before → after with the machine in the repo's learnings file.

## Workflow for re-proving changed code

1. Freeze the target, keep the previously proved version reproducible, and retarget mechanically (`reference/proof-campaigns.md`, "Order of work").
2. State every changed contract with `sorry`, list the open items, and split them into packages that own separate files.
3. Treat a proof that won't go through as a possible code bug; check the code on that path before weakening a contract.
4. Record every contract change with its cause. Gate against the base with the complete list of intended changes (`reference/verification-tooling.md`, rule 10).

## Workflow for landing in a shared repo

1. **One risk class per PR.** Proof edits, a new tactic, and checking or measurement tooling go in separate PRs. Bundled tooling draws the heaviest review and holds back low-risk proof speedups that could land on their own.
2. **Read the PR thread and the landing queue before every push.** A maintainer may already have posted a hold, review findings or a landing order; pushing without them answers the wrong question.
3. **Run CI's exact command, then `git status --porcelain`,** before pushing. A clean-tree check failed an otherwise green run on an untracked `__pycache__/` that a Python self-test wrote (`sys.dont_write_bytecode = True` before importing the tool).
4. **Treat recurring noise as a bug.** A stray file or warning that keeps coming back gets its cause fixed, not deleted again.
5. **Rebase before CI does.** On a fast-moving main the box reported "does not merge" because main had deleted a file the PR edited. Rebase right before pushing, carry an edit to a deleted file into its replacement or drop it, and check that nothing main removed comes back (`git diff --diff-filter=A origin/main...HEAD`).
