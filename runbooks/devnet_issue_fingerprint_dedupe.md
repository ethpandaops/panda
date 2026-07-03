---
name: Fingerprint and Dedupe Devnet Issues
description: Build a stable fingerprint for an Ethereum devnet issue and decide whether two issues are duplicates, variants of one root cause, or separate — identity that survives restarts, snapshots, and repeated watch runs, and prevents one-issue-per-node inflation. Use when emitting an issue or comparing it against prior issues.
tags: [devnet, issue, fingerprint, dedupe, identity, triage]
triggers:
  - are these two devnet issues the same bug
  - deduplicate issues across watch runs or snapshots
  - same error on many nodes one issue or many
  - stable issue id across devnets
---

Owns issue IDENTITY: how to name and compare issues across snapshots, devnets, and
repeated runs. Called by `runbooks://devnet_issue_collation` and
`runbooks://devnet_issue_root_cause`; it fills the `fingerprint` block of the issue
record in `runbooks://devnet_issue_contract`.

## Inputs
Required: category/layer/spread, the active fork (or boundary) nearest `first_bad`, the
first bad artifact, and component inventory (role, client, image, version).
Preferred: disagreement positions, co-present signals, setup summary, and prior issue
records when deduping.
If inputs are thin, still emit a fingerprint, set confidence low, and list which fields
would stabilize it.

## Output

The `fingerprint` block, reasoning first:

```yaml
fingerprint:
  rationale: >
    Same normalized reveal-400 error and component signature (builder:buildoor+vc:teku)
    as issue-3, but at a different fork boundary — variant, dimension: fork epoch.
  key: "v1:builder-path-degraded:tooling:gloas:builder-reveal-fails:subset:builder:buildoor+vc:teku:reveal-400"
  decision: variant            # new|duplicate|variant|insufficient-context
  matched: "issue-3"
  variant_dimension: "fork boundary (gloas at epoch 3 vs 8)"
  confidence: medium           # scale: runbooks://evidence_discipline
```

## The key

Deterministic and intentionally boring — stable across restarts, sandbox/snapshot ids,
timestamps, and repeated runs:

```text
v1:<category>:<layer>:<active_fork_or_phase>:<symptom_class>:<scope>:<component_signature>:<artifact_signature>
```

- `symptom_class`: the normalized first symptom, e.g.
  `head-advances-finality-stalled`, `gloas-builder-reveal-fails`,
  `engine-newpayload-invalid`, `payload-attestation-missing`,
  `client-service-restarting`.
- `component_signature`: `role:client` families, lexically sorted, joined with `+` —
  `builder:buildoor+cl:lighthouse+el:geth`, `builder:buildoor+vc:teku`, `multi-client`.
  The sort is plain string order — two agents fingerprinting the same issue must
  produce the same key.
- `artifact_signature`: a short stable phrase for the normalized artifact/error shape.

Lowercase, hyphen-separated. Normalize by stripping UUIDs, container ids, ports,
timestamps, sandbox/snapshot ids, peer ids, per-run numeric suffixes, and validator
indices (unless the validator range itself is the issue).

## Identity fields

Identity is built from: category/layer/spread; active fork and boundary relation;
normalized error text; role + client families (exact versions only when material);
disagreement axis; presence/absence of configured demand, builders, relays, VCs, fork
features; reproduced root-cause class when available.

Identity excludes (context only): sandbox/snapshot/enclave/container ids; wall-clock
timestamps; a node index alone (`cl-3`); ports, localhost URLs, temp paths; one-off log
ordering; speculation not yet backed by reproduction or trace.

## Grouping rules (first cause, not loudest symptom)

- **Duplicate:** same category/layer/symptom class, same fork or boundary relation,
  same component signature, same normalized first artifact, no meaningful topology
  difference — and the later issue is explained by the same first artifact. Record the
  new occurrence's evidence under the matched issue.
- **Variant:** same likely root-cause family via a different topology, client version,
  fork boundary, load condition, or component pair — or the same bug on a different
  devnet/chain id, or the same code path via a different trigger. Name the
  `variant_dimension`.
- **Separate:** different first bad artifacts where neither explains the other; a
  chronic co-present signal; different layers with no causal link; missing-demand vs
  runtime-failure-under-demand. A separate issue is emitted with `decision: new` —
  there is no `separate` value in the decision enum.
- **Insufficient-context:** no first bad artifact, no setup, no affected components, or
  only a prose title without evidence.

**One issue per cause, not per node.** When the same normalized error spans nodes, sort
by role/client/first-observed, check for a shared first artifact, and emit ONE issue
with multiple affected components — `spread` carries the extent, per-node views live in
evidence. Split per node only when failures are independent (different first errors,
start windows, or client versions/roles with no shared timeline).

## Procedure

1. Normalize the incoming issue (strip run-specific values).
2. Build the key.
3. Compare the exact key against prior records; on a miss, compare the identity
   dimensions above (first-artifact class, boundary relation, component signature,
   disagreement axis, topology dependency, error signature).
4. Decide `duplicate | variant | new | insufficient-context` and write the rationale so
   downstream work skips known duplicates.

## Self-Check

Before returning:
- The key contains no sandbox/snapshot/enclave id, port, timestamp, or raw node index.
- Node/service names appear in evidence, never in identity.
- Confidence is set per `runbooks://evidence_discipline`, with the capping criterion named.
- Duplicates preserve the new occurrence; variants name the dimension rather than
  inventing a new root cause.
