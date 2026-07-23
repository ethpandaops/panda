---
name: Drive Buildoor Per-Slot Action Plans
description: Script a devnet buildoor builder instance's behavior for specific future slots — hot-patch the built execution payload with a jq transform (e.g. .gas_limit = 300000000), rewrite bid or envelope messages before re-signing, override bid values, withhold reveals — then verify what each slot actually did. Covers discovering which buildoor instances a devnet runs, validating expressions with test-transform before applying, plan freeze semantics (409 conflict on past or frozen slots, target at least 2 slots ahead), authenticatoor bearer tokens for mutations (401 unauthorized), and reading per-slot results.
tags: [buildoor, devnet, epbs, builder, action-plan, jq]
triggers:
  - hot patch buildoor payload gas limit on a devnet slot
  - set a jq transform on future slots in buildoor
  - buildoor action plan update 409 slot frozen or in the past
  - which buildoor builder instances run on this devnet
  - buildoor 401 unauthorized bearer token action plan
  - did the transformed slot build a bid check slot results
---

Owns scripting a buildoor builder instance's per-slot action plan from the CLI and
verifying the outcome. The action plan is the sanctioned way to alter what a builder
produces on chosen slots — a jq transform on a future slot replaces any temptation to
patch payload handling into the builder itself. Judging whether the resulting network
behavior is healthy stays with `runbooks://debug_ethereum_network`.

## Model

A devnet runs one buildoor instance per builder host (named `<cl>-<el>-N`, e.g.
`prysm-ethrex-1`). Each instance keeps a sparse per-slot **action plan**: absent
slots inherit the instance's global config; a planned slot overrides it. Transforms
are operator-supplied jq programs applied to the object's JSON form when the slot
executes:

| Transform  | Applies to                      | Effect                                                             |
| ---------- | ------------------------------- | ------------------------------------------------------------------ |
| `payload`  | built execution payload         | feeds both the bid commitment and the reveal (block hash re-synced) |
| `bid`      | bid message just before signing | re-signed after rewrite — validly signed but customized             |
| `envelope` | envelope message before signing | re-signed; lets the reveal diverge from the bid commitment          |

Plans **freeze** when execution for their slot can begin, roughly 1 slot ahead;
edits to past or frozen slots fail with 409. **Target slots at least 2 ahead**
(`--slots +2` or later).

The same operations serve every surface: the CLI commands shown below, and
sandbox Python via `from ethpandaops import buildoor` (`list_instances`,
`test_transform`, `set_transforms`, `get_slot_results` — see the buildoor
examples index for filled scripts).

## Procedure

1. **Discover.** `panda buildoor networks` lists devnets with a buildoor
   deployment; `panda buildoor instances <network>` lists that devnet's builder
   instances live from its overview service. Commands address instances by the
   short name shown there.
2. **Anchor slots.** Slot arguments accept absolute numbers or offsets against the
   instance's current slot: `+N` ahead, `+-N` behind. Offsets resolve from the
   instance's own overview; an unsynced instance reporting `current_slot=0`
   rejects offsets — pass absolute slots or pick a healthy instance
   (`panda buildoor overview <network> <instance>` shows `current_slot`).
3. **Validate the expression first.** test-transform runs the exact production jq
   path against a sample object without touching any plan — a captured artifact
   when `--sample-slot` names one, else a template:

   ```bash
   panda buildoor test-transform <network> <instance> payload '.gas_limit = 300000000'
   ```

4. **Apply to future slots.** Mutations need a bearer token (see Auth):

   ```bash
   panda buildoor transform <network> <instance> --slots +2,+3 \
     --payload '.gas_limit = 300000000'
   ```

   `--from +2 --to +10` targets a range; `--payload ''` clears that one
   expression; `--clear` removes all three. For non-transform plan categories
   (bid values, reveal gating, builder-api overrides), pass raw PlanUpdate JSON:
   `panda buildoor plan-update <network> <instance> --updates '[{"slots":[N],"set":{"bid.bid_value_gwei":5000}}]'`.
5. **Verify.** `panda buildoor plan <network> <instance>` shows planned slots
   (defaults to current−8..current+24); after the slot passes,
   `panda buildoor results <network> <instance> --min-slot N --max-slot M`
   returns the attempt-level outcome — build, bids, submissions, reveals,
   inclusion — plus the frozen plan the slot actually ran with. A runtime
   transform failure fails that slot's construction loudly and is recorded there.

## Auth

Reads are open — no credential involved. Mutations are credentialed one of two
ways:

1. **Proxy-credentialed (default).** When a connected panda proxy advertises
   buildoor, mutations route through it with no flags: the proxy holds a CF
   Access service token, mints a JWT from the devnet's authenticatoor
   (`auth.<network>`), and injects it. Buildoor's audit log shows the service
   identity; the acting human stays attributed in the proxy's audit log.
2. **Personal token (override).** Pass `--token` (or `PANDA_BUILDOOR_TOKEN`)
   with a bearer minted in a browser at
   `https://auth.<network>.ethpandaops.io/auth/token` (SSO-gated). This goes
   direct to the instance and puts your own identity in buildoor's audit log.

## Failure modes

| Symptom                                        | Cause and remedy                                                                     |
| ---------------------------------------------- | ------------------------------------------------------------------------------------ |
| 409 "slot … frozen" / "in the past"            | plan froze (~1 slot ahead); retarget ≥2 slots ahead                                  |
| 401 unauthorized                               | no proxy advertises buildoor and no/expired personal token — see Auth                |
| 400 on apply                                   | invalid jq or unknown PlanUpdate field; reproduce with test-transform                |
| "current_slot=0 — cannot resolve relative"     | that instance's beacon node is unsynced; use absolute slots or another instance      |
| slot passed, no bid in results                 | transform may have failed at construction — read the slot's result record            |

## Self-Check

- Expression validated with test-transform before applying.
- Target slots ≥2 ahead of the instance's current slot at apply time.
- After execution, the slot's result record shows the frozen plan you set and the
  expected outcome (or the recorded failure).
