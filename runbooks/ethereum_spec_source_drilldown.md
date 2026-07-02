---
name: Drill Into Specs and Client Source
description: Resolve protocol specs, EIPs, and the exact consensus/execution client source at the running commit for an observed devnet issue, and produce bounded source findings for a reachability trace. An escalation step for protocol-semantics and client-code questions — runtime evidence comes first.
tags: [ethereum, specs, eip, source, client, code, commit]
triggers:
  - which spec rule or eip governs this behavior
  - inspect client source code for this error
  - resolve the exact commit the client image is running
  - is this client behavior a spec violation
---

Owns SOURCE findings: exact spec/source lookup and local code-path analysis. It shows a
path exists in the code — whether this network executed it belongs to
`runbooks://devnet_issue_reachability_trace`, which every blame-relevant finding is
handed to. Escalate here only after runtime evidence points at protocol semantics or a
client implementation path.

## Inputs
Required: the issue record with its evidence window; affected components with
client/image/version; the active fork at `first_bad`; and concrete artifacts (exact
error strings, RPC methods, block roots, payload ids, tx hashes, blob commitments,
validator/builder indices).
Preferred: reproduced evidence (or an explicit failed-reproduction statement) and any
candidate client pair already implicated.

## When to escalate

Run for: client-specific failures; cross-client disagreement; fork-boundary behavior;
invalid-block, payload-validation, engine-API, or state-transition failures;
blob/PeerDAS/PTC/builder/ePBS semantics questions; a reproduced issue pointing at a
concrete error string, stack path, RPC method, duty, or artifact; or any report that
would otherwise claim a client/protocol bug.

Close with runtime evidence instead for: missing load generators; expected empty blocks
on idle networks; stopped containers, OOMs, disk exhaustion, host-level lifecycle
failures; missing Prometheus/Dora/OTel; Kurtosis args-file mistakes.

## Resolve protocol context

1. Determine the active fork at `first_bad` (fork epoch, slot timing, feature flags)
   from the network config or beacon spec, and identify the specific artifact per
   `runbooks://ethereum_protocol_model`.
2. Read protocol material via Panda surfaces before general web browsing: search
   the eips index for the EIP number or topic, the consensus-specs index for the
   fork field or artifact, and the runbooks index for the symptom.

3. State the rule under test in plain language — the EIP/spec area and the invariant,
   without dumping long spec text.

## Resolve the exact runtime source

Inspect the code the network actually ran. Resolve the running image first, strongest
evidence first: (1) image digest + OCI labels; (2) version endpoint / startup log with
commit; (3) image tag containing a git SHA or release tag; (4) ethpandaops inventory /
network source metadata; (5) release notes mapping a tag to a commit.

```shell
kurtosis service inspect <enclave> <service>
docker image inspect <image>
curl <beacon-api>/eth/v1/node/version                 # CL commit
curl -s -X POST <rpc> -H 'content-type: application/json' \
  --data '{"jsonrpc":"2.0","id":1,"method":"web3_clientVersion","params":[]}'   # EL commit
```

If the exact commit cannot be resolved, say so; inspecting a release tag or nearest
commit is allowed, but the finding's confidence drops and line-level blame at an
unresolved rev is off the table.

## Drilldown procedure

1. Fetch the implicated repo at the exact commit or release tag.
2. Search FROM observed evidence — the exact error string, RPC method, log field, spec
   artifact name, validation function, or duty path — rather than broad keywords.
3. Trace local reachability from the runtime input to the observed error: the entry
   point/RPC handler, the validation/state-transition/fork-choice function, the branch
   condition matching the evidence, the emitted error. A path counts only if the
   observed service could execute it under the reproduced conditions.
4. Classify code vs active rule as `spec_comparison`:
   `matches-spec | mismatches-spec | ambiguous-spec | code-path-unresolved | wrong-component`.
5. Preserve rejected paths — a file that mentions the right words but is unreachable is
   recorded as rejected so later reports don't overfit to string matches.
6. Hand blame-relevant findings to `runbooks://devnet_issue_reachability_trace`.

## Output

Reasoning first, then the handoff fields the trace needs:

```yaml
source_finding:
  summary: >
    The reveal 400 originates in buildoor's envelope serializer: it emits a pre-Gloas
    envelope version when BPO epoch 2 overlaps the Gloas boundary. Branch condition
    matches the reproduced config (BPO at 2, Gloas at 3). Spec comparison:
    mismatches-spec (EIP-7732 envelope versioning).
  component: { client: buildoor, role: builder, image: "ethpandaops/buildoor:0.3.1" }
  commit: { repo: "github.com/ethpandaops/buildoor", commit: "a1b2c3d", status: exact }   # exact|release-tag|nearest-known|unresolved
  entry_artifact: { kind: log, value: "Failed to submit reveal ... status 400", ref: "kurtosis service logs devnet-1 buildoor -n 3000" }
  source_path: { file: "pkg/reveal/envelope.go", function: "serializeEnvelope", branch_condition: "bpo_epoch < gloas_epoch" }
  spec_comparison: mismatches-spec
  rejected_paths: [ { file: "pkg/bid/creator.go", reason: "logs the error but downstream of serializer" } ]
  confidence: medium           # scale: runbooks://evidence_discipline
  citations: ["git show a1b2c3d:pkg/reveal/envelope.go", "eips index search: 7732"]
```

If `entry_artifact`, `source_path`, or the branch condition cannot be filled, lower
confidence and queue source/trace feedback rather than claiming source blame.

## Anti-patterns

- Inspecting the default branch instead of the running commit.
- Searching broad terms like `payload` and calling the first hit causal.
- Quoting spec text without tying it to slot, fork, and runtime evidence.
- Treating the majority implementation as correct by default.
- Calling a file the root cause when it only logs a downstream error.
- Ignoring the paired layer — many CL symptoms originate in EL responses and vice versa
  (CL/EL matrix in `runbooks://debug_ethereum_network`).

## Self-Check

Before returning:
- Panda spec/EIP/example/runbook surfaces were tried before general web lookup.
- The runtime image and commit status are explicit.
- Inspected files were selected from observed artifacts.
- Rejected string-match paths are preserved.
- Every blame-relevant finding is handed to the reachability trace.
