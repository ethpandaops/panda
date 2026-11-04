# Build and proof-speed playbook

From eth-act/clifv, 2026-10-28 to 31 (Lean 4.34.1, about 5,000 modules). Measured results:
- A fresh 16-core orb got to a working compiler in 23.9 min instead of 75.7.
- A cold build of the compiler took 425 s instead of 1,370 on 32 cores.
- Whole-tree CPU went from 44.2 h to 35.8 h, with more modules.
- The slowest proofs got 5–50× faster.
- No statement or axiom changed.

## Order of work

1. **Fix the pipeline first.**
   - Check that the CI cache restores the exact commit and actually refreshes. One NUL byte in a Lean string literal crashed Lake's cache packer (leantar) for 11 days.
   - Make sure nothing replans or silently rebuilds.
   - Make sure memory caps match reality.
   These fixes are cheap and often worth hours.
2. **Make the build observable.** Record wall time, CPU and peak RSS per module on every build, in a table checked into the repo. Memory caps, scheduling, CI planning and regression spotting all depend on it.
3. **Profile the top 10 modules,** then fix shared tactics.
4. **Then the import graph,** which only shortens the longest chain.
5. **Then the agent edit loop.**

## Build infrastructure

- **Memory-aware scheduler instead of plain `lake build`.** Plain `lake build` starts every ready module at once. Instead:
  - run one memory-capped `lake build <M>` per module;
  - pack modules within a memory budget, longest critical path first;
  - backfill only modules that can't delay a reserved one;
  - run a heavy or retried module one at a time, holding only its own cap, with light modules beside it. Draining the machine for it cost about 140 min idle on one cold build; fixing that took a test build from 70.8 to 54.9 min.
- **Ask Lake once what is out of date.** One `lake build --no-build <all modules>` restores everything cached and lists what's stale. A warm tree with nothing stale went from 27.5 min to 1.9 s.
- **Share one Lake artifact cache across worktrees** (`LAKE_CACHE_DIR`, hard links on the same filesystem).
- **Keep the per-module time and memory table fresh.** Modules without a record got default caps and caused 36 of 37 memory-cap kills. A record that's too small (127 MB for a 4.7 GB module) causes kill-retry loops.
- **Lean `.olean`s are not byte-reproducible.**
  - When CI shards each build a shared import, pick one copy on purpose (the one most downstream work was built against). Otherwise everything built on the other copy rebuilds, uncapped. One module went from 12 s to 3 h this way.
  - Linter warnings embed absolute paths in `.olean`s, so turn build-only linters off with `weakLeanArgs`.
  - `Elab.async=false` makes `.olean`s reproducible but is 2.5–2.8× slower.
- **`LEAN_NUM_THREADS` doesn't cap async elaboration.** Modules ran at 750% CPU. `lean -j2` cut CPU about 3× for one module alone but doubled its wall time (RuleBitops3: 178 s / 2,390 CPU-s → 376 s / 720 CPU-s). On a CI-shaped build (24 modules, 3 jobs on 4 CPUs) it was not worth it: wall −3.4% (within noise), CPU −8.4%, individual modules ±40% both ways. Not adopted.
- **Keep the timing table honest automatically.** After each build, flag modules more than 2× (and 60 s / 1 GB) off their record, and write measurements back (clifv: `scripts/peaks-update.py`, drift section in the full-gate report). Records that are too small cause kill-retry loops; records that are too large waste the memory budget (stale 8 GB caps on 3.7 GB modules left 11 of 16 cores idle until refreshed).
- **Heavy modules hold their cap, not the machine.** A heavy or retried module runs as the only heavy one, with light modules beside it within the budget, instead of draining everything.
- **Caches only help when they're recent.** A cache that predates a big change was slower than no cache (32.5 vs 23.9 min). Per-commit publishing isn't worth it when main moves several times an hour; restore the newest main artifact and rebase onto its commit when you can.

## Proof speed

- **Profile before guessing.** The obvious theory (failing `first` branches) was wrong; profiles found the real causes.
- **Fix the shared tactic, not each proof.** A few lines in a shared tactic fixed dozens of modules.
- **Close recurring goal shapes before the general search.** Teach the fast path the identities it misses, and check them before a type split that multiplies goals.
  - Five shift lemmas took a module from 4,541 s to 152 s.
  - One width-generic lemma before a 2,940-way split: 93 G → 0.42 G heartbeats.
- **Limit what `simp` walks.**
  - Simplify only the head computation of each bind.
  - Leave the alternatives of a non-reducing `match` alone.
  - Use one `simp only` per normalisation step, not an alternating repeat.
  - Skip `rw` lemmas whose head constant doesn't occur.
  - Results: 4,516 → 917 s, 1,257 s / 14 GB → 654 s / 10 GB, and −50% over 166 modules.
- **Unify at reducible transparency** where that's enough. A failing `assumption`/`refine` at default transparency unfolded memory writes exponentially: 815 s → 16 s.
- **Heartbeat limits can lie.** Tactics that isolate a search can reset the counter (93 G of work under a 4 M limit). Expose hidden cost with an option such as `-Dkbv.reportHeartbeats=N`.
- **Evaluate index lookups by table, not by unfolding.** Functions like `binaryOfIdx?` compile to deep `dite` chains; unfolding them in `simp` rebuilt their matchers on every visit (half of a tactic's time). A dsimproc that looks the numeral up in the table, plus a simp set prebuilt once as an attribute instead of re-elaborating ~80 lemma names per call, took RuleBitops3 205 → 94 s and halved CPU (clifv #855, #862).
- **Head-only normalisation works only outside case-split loops.** Simplifying just the head of each bind between interpreter steps is a large win; using it inside the split loop breaks proofs. Do one full normalisation before splitting.
- **Precompiling custom tactics rarely pays.** Measure the interpreted share first (sample stacks with eu-stack; perf's dwarf unwinding lost the caller in ~47% of samples). In clifv it was 0–8%, below a 10% bar, so it wasn't tried (#900).
- **Core lemmas can add axioms.** A bridge built from core `find?` lemmas pulled in `Quot.sound`; a hand-written propext-only bridge avoided it. The signature diff caught it.
- **Kernel evaluation:**
  - scan Lists rather than Arrays;
  - use structural recursion;
  - evaluate a concrete pipeline once per certificate;
  - never reduce a whole generated program;
  - replace linear table scans with a lookup proved equal to them (−25 to −70% on decide-table modules).
- **Split heavy modules** when the peak comes from several declarations together. It doesn't help when one declaration is the peak.

## Symbolic-execution proofs (eth-act/ml-dsa-fv)

From eth-act/ml-dsa-fv, 2026-11-01 to 02 (Lean 4.33, about 600 modules, EVM bytecode proved by symbolic execution of straight-line blocks). Same orb, adjacent clean builds: 716 s / 8,104 CPU-s → 303 s / 4,051 (0.42× wall, 0.50× CPU). .olean bytes 4.49 → 1.52 GB. Summed per-module CPU, profiled: 8,290 → 4,201 s. End-to-end gate from the first commit to the final one: 0 statements changed or removed.

- **Kernel cost is superlinear in the length of a simp-built term.** One block of 85 / 170 / 340 instructions took 1.65 / 7.75 / 78 s; perf showed 69% in `expr_eq_fn` via the equiv manager. Folding the block in 32-instruction chunks: BodyA 93 → 22 s, Trabs 125 → 55 s. Chunk first; it is a one-line change in the shared tactic or generator.
- **Proof by reflection beats simp for straight-line code.** A verified evaluator over a reified instruction list, checked by the kernel once per block: BodyA 22 → 14 s, Trabs 32 → 21 s, Keccak blocks 40 → 20 s; making it the default for every block call cut a clean build by 11%. What made it pay:
  - cover nearly every opcode before measuring (13% → 98% coverage); every unsupported opcode falls back to the slow path;
  - build the bridge simp context once per call, not once per obligation (25–60 ms each, 7 obligations);
  - keep `Bool.true_eq`/`false_eq`-style lemmas out of the bridge simp set (recursion-depth failures);
  - a fallback must cost nothing: decide up front whether a block reflects.
- **Reflection doesn't always pay; measure the clean build.** Extending it to symbolic return jumps passed the gate and sped up the sample modules, but made the clean build +3.8% (one module 9 → 23 s). Not merged.
- **A cheap literal-bound discharger instead of `omega` in the hot tactic.** About 3 omega side goals per instruction at ~5 ms each; most were `literal < literal` or non-overlap of literal offsets. Omega calls 38.8k → 10.3k, auxiliary constants 98k → 40k, HintBlocks 90 → 64 s. Omega's `Decidable` instance synthesis was the 300 s "typeclass inference" line of the profile; it can only be reduced by calling omega less.
- **Rewrite once instead of discharging per step.** An aliasing check per memory store (about 128 omega calls per NTT body) became one `rw` of the read address plus literal-offset store lemmas: −35% CPU on 10 modules, then −20% on 20 more with the same fix.
- **Stop the kernel from evaluating big literals.** simp's projection reduction on a state literal made the kernel try struct-eta and evaluate every `natToBytesPadded v 32` in memory. Pre-simprocs with non-rfl projection lemmas: Fix 64 → 33 s, Tbl 78 → 54 s. Rewriting a `storeWords` into an explicit chain before the block tactic: Tbl 43 → 19 s.
- **Scan the whole tree for failing `isDefEq`.** `trace.profiler` over every module, listing failing defeq nodes ≥ 0.5 s, found 15 modules of one-line fixes, −180 CPU-s: `rwa` that runs `assumption` over the wrong hypotheses first (replace with `rw …; exact h`), `▸` (use `rwa`), `clear` unrelated hypotheses before `omega`, `with_reducible exact`, `congrArg` instead of `simp`. A bit-list reflection (`decide +kernel` per word) took one module 51 → 3 s, another 106 → 12 CPU-s.
- **Per-call costs beat per-module fixes.** The top 10 modules were only 15% of total time; the wins that moved the clean build were all in the shared block tactic.
- **Critical path:** splitting one table-validation module into parallel chunks and narrowing imports took clean builds 373 → 311 s at +2% CPU.
- **Didn't work:** Lean 4.34/4.35 (lean4#14806 replaced the equiv-manager union-find; BodyA hit kernel deep recursion, >12 min against 19 s; 4.35 also broke the EVM semantics dependency); `shareCommon` before the kernel (6.15 → 6.38 s); a compiled simproc plugin (small win, but a native plugin and lakefile change for what lemmas nearly matched); a prebuilt simp set (13 ms per call, not the cost); per-index lookup lemmas (creating a lemma costs about as much as using it); a central AC closer for reflection leftovers (fallbacks 183 → 174, CPU flat); turning Mathlib linters off (1–4%).

## Import graph

- **Remove unused imports** with a tool that:
  - keeps imports needed only for notation, macros, simp sets, `open` or quoted names;
  - protects consumers outside the libraries;
  - preserves the load order of equal-priority instance providers.

  On Lean 4.34, `lake shake` only handles `module` files.
- **Re-check periodically.** 201 unused imports came back within two days.
- **The import tool only sees modules that exist when it runs.** On a fast-moving main, modules added after its base can need an import it removed (clifv #894: 3 of 303 edits broke later modules; 6 more conflicted with main). At merge time: rebase, drop edits to files main changed, build everything that imports an edited file (including new modules), and diff signatures against main at the same commit. A "no module loses an import" rule measured against the trimmed tree misses new modules; measured against main it is far too strict (would have dropped 121 files). The build plus the signature diff is the real check.
- **Don't freeze others' merges for it.** Rebase late, build only the delta, merge, and let the next full gate on main be the full check.
- **Move widely used lemmas into low modules** to shorten import chains. That cut a closure's critical path from 0.57 to 0.16 h.
- **Import work shortens the longest chain but doesn't reduce total work.** It pays when cores are spare; on small machines only proof speedups help.
- **Lean's module system (≥ 4.35)** should make a proof-only edit rebuild 1 module instead of 300. On 4.34 it gave only 6–12% fewer rebuilds, so it was parked.

## Model the build

- The longest dependency chain, using recorded seconds, is the floor with unlimited cores.
- Total work divided by core count is the floor on small machines.
- Use these floors to choose between import surgery and proof speedups, and keep modelled numbers separate from measured ones.

## Agent edit loop

1. **Language server first** (lean-lsp-mcp). It reports the edited declaration first: 0.2–27 s even in a file whose full check takes 17 min.
2. **turbo-lean** for one theorem in a heavy file (about 0.3 GB instead of 12 GB). It stubs earlier theorems with `sorry`, so a pass is not a module proof.
3. **`lake env lean <file>`** only for small files.
4. **Before a commit:** build the module and its importers with the scheduler. Neither fast loop re-checks other files.

## Measurement hygiene

- **On Amp orbs, wall-clock numbers are valid only while the thread is active.** An orb pauses when its thread goes idle; a running build stops but `time.time()` keeps counting (one A/B lost 41 min this way), and a resume kills processes started from the shell. Keep the measuring thread polling, run long builds as supervised orb services, and log `date +%s` against `/proc/uptime` every few seconds to detect pauses.
- Compare before and after on the same machine in the same session, with the module run alone. Contended timings misled: −77% contended turned out to be flat when run alone.
- **Alternate before and after builds.** One orb's clean build of the same commit drifted from 716 s to 1,089 s over a night. Only adjacent pairs (old, new, old, new) are comparable; a "reality check" against an older number that wasn't a clean build of the same code once concluded, wrongly, that the wins didn't add up.
- **Keep a control library in serial tables.** A library no change touched (EvmProof: 48.4 vs 48.7 s) shows whether two serial runs are comparable.
- **A clean build means `rm -rf .lake/build` with dependencies kept,** sequential, on an idle orb. Report wall, CPU and peak RSS.
- Use main's scheduler for both trees, but say so when its memory table under-caps the old code, and report the time lost to kill-retries separately.
- Every speed PR needs a statement/axiom diff over all constants, an axiom audit, the importer cone built, and byte-identical output where it applies.

## Prompts that work for Lean speed work

In Lean the hot loop is usually the kernel or `simp` walking a term, so "data layout" means how definitions are shaped for reduction and what `simp` and unification may look at.

| General perf idea | Lean version | Evidence (clifv) |
|---|---|---|
| Data-oriented design | Shape definitions for the kernel: structural recursion, Lists not Arrays in kernel-evaluated code, no deep `if`/`match` chains on hot paths. | Unfolding `binaryOfIdx?`'s nested `dite` chain was half of `lhs_step`'s time (#855). |
| Flat arrays and indices, not hash tables | Kernel-evaluated code: a lookup proved equal to the slow one (strided or tree index). Compiled checkers: the reverse, a `HashMap` behind a lookup parameter. | Strided term lookup instead of a 2,482-entry list walk: −57% (#824, #870). `lean-link` 2 h 26 min → 12 min with hash maps. |
| Bitsets, not arrays of bools | `Nat`/`BitVec` bitmasks; the kernel does `Nat` arithmetic natively (GMP), while a `List Bool` is walked cons by cons. | A one-pass bit-set Nodup over 518 rule ids, plus a name-generic lemma instead of unfolding a lookup at each literal name: IselContract 180 s → 7 s (#928). |
| Caching is cope; redesign | Don't raise `maxHeartbeats`, add memory or lean on caches; find why it is slow. | Cache, `-j2` and bigger-machine experiments gave little; tactic and table fixes gave 2–50×. |
| Structure of arrays | Split data by how it is queried; evaluate a pipeline once per certificate, not once per fact. | Seven `decide` theorems each re-ran the whole pipeline. |
| Don't allocate in the loop | Bind tables with `let` outside a lambda; prebuild simp sets as attributes. | A symbol map rebuilt per call: 67 min → 5 min. ~80 lemma names re-elaborated per call (#855). |

Say these to the agent:
- "Profile this first; where do the heartbeats actually go?"
- "Is this simp walking the whole hypothesis? Can it touch only the head?"
- "Why does the kernel unfold this definition here? Can it be reducible-only, or sealed behind an equation lemma?"
- "Can this lookup be proved equal to a cheaper one, then rewritten once at the top?"
- "Replace this List-of-Bool or membership scan with a `Nat` bitmask."
- "Close this recurring goal shape before the case split, not after."
- "Don't raise the heartbeat limit. Find why it needs more."
- "Fix the shared tactic, not this proof."
