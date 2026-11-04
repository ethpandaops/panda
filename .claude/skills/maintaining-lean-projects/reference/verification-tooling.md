# Rules for verification tooling

"Verification tooling" means anything whose answer decides whether a proof counts: axiom audits, sorry/axiom gates, statement or signature diffs, caches that store "verified", CI gate jobs, and agent check loops that report success.

Source: the Codex review of Th0rgal/turbo-lean#275, which integrated the clifv changes from turbo-lean#273 (2026-10-30). It found six P1 findings. Each one would have let something pass as verified that wasn't, and each was fixed with a test before merge.

## 1. Key a cached verdict by everything that decides it

- **Bug:** whole-theorem cache entries were namespaced by axiom policy, but per-`have` sub-entries ("fission" keys) were not. A proof verified under a wide allowlist was reused under a strict one without being audited again.
- **Rule:** the key of a cached "proved" result covers the toolchain, sources, options, imports' outputs **and the trust policy**: allowed axioms, `native_decide`/`bv_decide` allowance, sorry handling. Check every cache level, not only the top one.
- **Check:** can two runs with different policies read each other's entries?

## 2. One verification entry point; fast paths may not skip the gate

- **Bug:** the "re-check modules that import the edited one" step ran only in the CLI's `main`. The MCP `agent_step` path called the evaluator directly and returned success for an edit the CLI rejected.
- **Rule:** every path that can report success (CLI, MCP, HTTP server, batch, CI) calls the same gate function. A loop that checks less is labelled a dev loop and never reported as a gate.
- **Check:** list every caller that can return "ok". Do they all reach the same final check?

## 3. One source of truth for the trust policy, decided by Lean

- **Bug:** a Rust substring test (does the pattern contain `bv_decide`?) disagreed with the kernel's glob match on generated axioms such as `*._native.*.ax_*`. The two gates admitted different proofs.
- **Rule:** decide axiom admission once, from Lean's own answer: `#print axioms` output, or the kernel's collected constants via a C-ABI or Lean helper. Don't re-implement it in another language, and don't grep source text for tactic names.
- **Check:** is there a second place that decides the same question?

## 4. "Everything downstream" includes executables and outside consumers

- **Bug:** the dependent-recheck graph was seeded with `lean_lib` targets only. A statement edit could break a `lean_exe` root (`Main.lean`) while the check passed.
- **Rule:** an importer closure includes `lean_exe` roots, audit files, packages that require this one (for example a `crate-proofs`-style package), and generated or test files outside the libraries.
- **Check:** which Lean files that import this project are not in any `lean_lib`?

## 5. The policy travels with the request, never through inherited state

- **Bugs:**
  - A parent process's config-loaded allowlist leaked into subprocesses through an environment variable and overrode the base-revision policy.
  - A remote verification server checked shards under its own allowlist, not the caller's, and the caller published the result under its strict key.
- **Rule:** pass the policy explicitly to every subprocess and remote request; the receiver enforces it or rejects a mismatch. Tag environment values set from config so they can be stripped, and don't let a variable set for one purpose decide another.
- **Check:** for each process boundary, where does the receiving side get its policy from?

## 6. Untrusted input never widens the policy

- **Bug:** the allowlist could be read from the PR's own `turbo-lean.toml`.
- **Rule:** read the policy from the base revision, or from configuration the change can't edit. Always reject `sorryAx` and `Lean.trustCompiler`, even under `*`.
- **Check:** can the change under test edit the file or variable that sets the policy? If CI runs the PR's own audit script and allowlist, it can. Mitigate with CODEOWNERS on the audit files, or run the base revision's audit script against the PR's build.

## Rules from the ml-dsa-fv proof gate

The gate in eth-act/ml-dsa-fv (`scripts/gate/gate.py check BASE HEAD`) checks every merge: a signature diff over all constants with per-constant axiom sets, an axiom policy over every constant, kernel replay of every module, the headline theorems, frozen artifacts and bytecode literals, and that every generator reproduces its files. It took seven review rounds and 41 regression tests. These rules came out of it.

### 7. An interrupted build can leave stale outputs that the next run trusts

- **Bug:** a killed gate build left orphaned `lean` processes that wrote newer `.olean` files afterwards. Lake 4.33 trusts input hashes, so later runs extracted stale outputs for 7 modules.
- **Rule:** kill the whole process group, take a lock, refuse to start while stale processes hold the worktree, check each `.olean` against its trace's output hash, and force a clean rebuild after an interrupted build.
- **Check:** what happens if the gate is killed mid-build and run again?

### 8. Compare axioms per constant, in both directions

- An extra `Classical.choice` dependency passed the global axiom policy (it is an allowed axiom) and was caught only by the per-constant axiom diff.
- The same diff flags a narrowing (a lemma going from three axioms to `{propext}`). That is strictly stronger, but a human signs it off; don't make the gate silently accept changes in either direction.
- Demonstrate the gate on planted changes: a weakened statement, an extra axiom dependency, a one-byte artifact edit, a moved lemma. Each must fail.

### 9. Every generator and every headline is in the gate

- A generator missing from the gate's list drifted: after the code changed, two generators crashed on hard-coded program counters while their committed output looked fine. Generators should read positions from the layout file, and the gate should rerun every generator.
- New top-level theorems (here the Glamsterdam builds' four final theorems per set) were covered only by the global axiom policy until they were added to the headline list. Add new headlines and new bytecode literals to the gate in the same change that adds them, with their statement digests.

### 10. Know which gate ran

- Changes to the gate's own tables (an expected memory size, a new generator entry) apply only when the HEAD revision's gate runs. Run both the base's gate and HEAD's gate when a branch edits the gate, and require an explicit flag such as `--allow-gate-change`.
- Give whoever reads the report the complete list of intended statement changes, and require a one-line justification for each one, tied to the code change behind it. The first v4 retarget gate "failed" on 1,258 unlisted constants that were all intended; the list, not the proof, was incomplete.
- The gate can't tell a deleted port from a broken one ("absent"). A human accepts those by name.

## Rules from a consensus formalization speed PR

A speed PR to a Lean 4.35 consensus formalization bundled proof speedups with a constant-diff gate, setup-script changes and timing tools. These rules came out of its review.

### 11. Exemptions name constants; never widen a class

- **Bug:** the constant diff skipped internal details from the start. When proof edits changed which reserved lemmas existed, the fix skipped every reserved name (`foo.eq_1`, `foo.congr_simp`, …) too, instead of allowing the few that changed by name. The reviewer then changed a private definition under an unchanged public one, and the diff passed with zero failures.
- **Rule:** accept an explained difference with a named allow entry, printed on every run (`--allow-added NAME`, `--allow-value NAME`). Never turn one exception into a skipped class. Keep comparing internal and private definitions that public types or values depend on.
- **Check:** for each skipped class, could a real change hide in it?

### 12. Test a gate end to end, with compiled mutations

- **Bug:** the diff's self-test fed hand-written rows to the comparison function and never ran the Lean exporter. Two compiled mutations passed: reordered universe parameters (the exporter wrote only their count) and the private definition above.
- **Rule:** plant each mutation in real Lean source, build it, run the exporter and the comparison, and require a failure. Export what identifies a constant exactly: universe parameter names in order, type, value, safety, axioms. Test with inputs where a plausible wrong implementation and the right one disagree, not ones the code is known to handle.
- **Check:** does any test exercise the exporter, or only the comparison?

### 13. Fix the invariant, not the reproducer

- **Bug:** a setup script rewrote a marked block in `~/.config/amp/AGENTS.md` by deleting from the start marker to the end marker, or to EOF for blocks written before the end marker existed. The review found it could delete text after a legacy block. The fix stopped at the legacy block's known last line, which handled the reported case; when that line was missing, it still deleted to EOF.
- **Rule:** state the invariant ("never delete text you can't identify") and make every path keep it: rewrite only a block whose terminator is present; otherwise warn and leave the file unchanged. Add a fixture for each path, including the one nobody reported.
- **Check:** list every branch of the fix; which one still violates the invariant?

### 14. Report-only tools state what they don't check

- A timing comparison used failed baseline rows and accepted tables from other machines; docs cited a regression table that was never committed. Neither decides what is proved, but a reader takes their output as evidence.
- **Rule:** fail an incomplete run loudly, compare only valid rows, record and check provenance (machine, CPUs, jobs) or say "same machine only", and write docs only about artifacts that exist.

## Process that found rules 1–6

The author ran the reviewer in a loop: review, fix each finding with a named regression test, review again, until it came back clean. That took four rounds on this PR. For soundness-relevant tooling, merge only after a clean independent review. A green build is not enough.

A common failure: the author runs one review, fixes its findings and pushes, and the maintainers' review then finds new problems in those fixes (rules 11 and 13). One round plus fixes is not a clean round: review the fixes too.

## Related failure seen in clifv

Removing unused imports changed which of two equal-priority instances Lean picked. Three theorems silently gained `Classical.choice`. The build passed; only a statement/axiom diff over every constant caught it. Run that diff on any tree-wide refactor, and make sure import tools preserve the load order of instance providers.
