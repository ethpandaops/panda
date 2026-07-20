#!/usr/bin/env bash
# Golden-query retrieval check for the runbook registry.
#
# Runs symptom-phrased queries against a RUNNING panda server (panda search
# runbooks) and asserts the intended runbook comes back. Run it after editing
# runbook descriptions/triggers or the search scoring — retrieval regressions
# are invisible until an agent quietly follows the wrong runbook.
#
# Two assertion tiers:
#   "query|expected"       strict — expected must be top-1. For queries where
#                          the wrong family is a genuinely wrong procedure.
#   "query|expected|top2"  expected must be in the top 2. For confusables
#                          between SIBLING runbooks of one pipeline family,
#                          where the neighbors cross-link and either answer
#                          resolves within one hop — a 0.00x coin-flip there
#                          is noise, not regression. Do NOT fix a top2 flip
#                          by mirroring the query text into a trigger; that
#                          makes this check measure string echo, not
#                          retrieval quality.
#
# Usage: scripts/runbook-retrieval-check.sh
# Requires: panda CLI configured against the server, jq.
set -u

# Ordered: explicit consumer phrases first, then intent phrases, then
# confusable pairs (queries chosen because a neighboring runbook is a
# plausible wrong answer — these guard the margins, not just the hits).
MATRIX=(
  # consumer phrases (mirrors of triggers)
  "run a devnet locally with kurtosis|kurtosis_devnet"
  "snapshot a devnet|panda_compute_kurtosis_lifecycle"
  "epoch aligned snapshot restore|panda_compute_kurtosis_lifecycle"
  "compute sandbox snapshot lifecycle|panda_compute_kurtosis_lifecycle"
  "can this component cause the failure|devnet_issue_reachability_trace"
  "engine_getBlobs returning empty|blob_propagation_vs_getblobs"
  "generate a devnet consensus bug report|devnet_bug_report"
  "is this root cause tunable or a bug to fix|devnet_issue_experiment_triage"
  "turn a devnet issue into an experiment campaign|devnet_issue_experiment_triage"
  "write an experiment goal from an investigation report|devnet_issue_experiment_triage|top2"
  "which client repo owns this fix|devnet_issue_experiment_triage|top2"
  # intent phrases (mirror runbook names/owns-lines)
  "public devnet context intake|public_devnet_context"
  "hosted devnet context|public_devnet_context"
  "build a kurtosis config from a public devnet|kurtosis_devnet_config"
  "running and reaching a kurtosis devnet|kurtosis_devnet"
  "debugging an ethereum network|debug_ethereum_network"
  "network_target contract|debug_ethereum_network"
  "neutral devnet watching facts only|devnet_watch"
  "fork model and health thresholds|ethereum_protocol_model"
  "evidence discipline citations|evidence_discipline"
  "collate watch observations into issues|devnet_issue_collation"
  "issue record shape|devnet_issue_contract"
  "issue fingerprint identity dedupe|devnet_issue_fingerprint_dedupe"
  "root cause investigation of a devnet issue|devnet_issue_root_cause"
  "reachability trace|devnet_issue_reachability_trace"
  "spec and client source drilldown|ethereum_spec_source_drilldown"
  "shape investigation follow-up tasks|devnet_issue_feedback_queue"
  "review this investigation plan before running it|devnet_issue_adversarial_review"
  "does this root cause conclusion hold up|devnet_issue_adversarial_review"
  "html bug board leaderboard with upvotes|devnet_bug_board_html"
  "render bug objects into an html bug board|devnet_bug_board_html"
  "clickhouse block number range time window|clickhouse_querying"
  "panda compute devnet lifecycle|panda_compute_kurtosis_lifecycle"
  "devnet node metrics prometheus peer count|prometheus_devnet_health"
  "dora and the beacon node disagree|reconcile_chain_sources"
  "query the devnet observability apis over a past window|devnet_observability_apis"
  "dora api filter param ignored same page|devnet_observability_apis"
  "same bytecode different gas across clients|debug_evm_execution_divergence"
  "invalid beacon block which client rejected it|tracoor_invalid_artifact_forensics"
  # confusable pairs (the near-miss neighbor must not win)
  "prysm is forked can you investigate|debug_ethereum_network|top2"
  "grandine is forked can you investigate|debug_ethereum_network|top2"
  "why is finality stalled on this devnet|debug_ethereum_network|top2"
  "finality lag thresholds offline stake fraction|ethereum_protocol_model|top2"
  "why did the devnet break|debug_ethereum_network|top2"
  "watch a running devnet live for a few epochs|devnet_watch"
  "watch a public devnet for a few epochs|devnet_watch"
  "restore a snapshot on remote compute|panda_compute_kurtosis_lifecycle"
  "read logs from a local kurtosis enclave|kurtosis_devnet"
  "clickhouse query slow timing out|clickhouse_querying"
  "make a chart of query results|sandbox_output_conventions"
  "periodic devnet status report incident roundup|devnet_bug_report"
  "are these two devnet issues the same bug|devnet_issue_fingerprint_dedupe"
  "turn watch observations into issue records|devnet_issue_collation"
  "inspect client source code for this error|ethereum_spec_source_drilldown"
  "is the blamed client really the trigger or a victim|devnet_issue_reachability_trace"
  "two datasources disagree which one to trust|reconcile_chain_sources"
  "vote_participation zero for every epoch|devnet_observability_apis"
  "scan devnet history over http without clickhouse|devnet_observability_apis"
  "how should findings be cited in a report|evidence_discipline"
  "one node stuck offline or out of sync|debug_ethereum_network"
  "should we tune this or fix the bug|devnet_issue_experiment_triage|top2"
  "is a service up according to metrics|prometheus_devnet_health"
  "execution_payload_block_number is 0 for every slot|ethereum_protocol_model"
  "fct_block_head min max execution_payload_block_number returns 0 0|clickhouse_querying|top2"
)

# Out-of-scope probes: nothing in the registry covers these. Informational
# only (absolute score thresholds are corpus-dependent) — they print the top
# hit + score so a human can spot the registry claiming expertise it lacks.
OUT_OF_SCOPE=(
  "kubernetes pod stuck in crashloopbackoff"
  "solidity reentrancy vulnerability"
)

pass=0 fail=0

for entry in "${MATRIX[@]}"; do
  q="${entry%%|*}"
  rest="${entry#*|}"
  want="${rest%%|*}"
  tier="${rest#*|}"
  [ "$tier" = "$want" ] && tier="top1"

  json=$(panda search runbooks "$q" -o json 2>/dev/null)
  top=$(echo "$json" | jq -r '.results[0].ref // "NONE"' | sed 's|runbooks://||')
  second=$(echo "$json" | jq -r '.results[1].ref // "NONE"' | sed 's|runbooks://||')
  score=$(echo "$json" | jq -r '.results[0].similarity_score // 0' | cut -c1-4)
  if [ "$top" = "$want" ]; then
    echo "PASS  ${top} (${score})  <- \"${q}\""
    pass=$((pass + 1))
  elif [ "$tier" = "top2" ] && [ "$second" = "$want" ]; then
    echo "PASS  ${want} at #2 behind ${top} (${score}) [top2 tier]  <- \"${q}\""
    pass=$((pass + 1))
  else
    top3=$(echo "$json" | jq -r '[.results[0:3][].ref] | join(", ")')
    echo "FAIL  top=${top} (${score}) want=${want} (${tier}) top3=[${top3}]  <- \"${q}\""
    fail=$((fail + 1))
  fi
done

echo
echo "out-of-scope probes (informational):"
for q in "${OUT_OF_SCOPE[@]}"; do
  panda search runbooks "$q" -o json 2>/dev/null |
    jq -r --arg q "$q" '"  \(.results[0].ref // "no result") (\(.results[0].similarity_score // 0 | tostring | .[0:4]))  <- \"\($q)\""'
done

echo
echo "== ${pass} pass, ${fail} fail =="
[ "$fail" -eq 0 ]
