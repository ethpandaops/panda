#!/usr/bin/env bash
# Golden-query retrieval check for the runbook registry.
#
# Runs symptom-phrased queries against a RUNNING panda server (panda search
# runbooks) and asserts the intended runbook comes back top-1. Run it after
# editing runbook descriptions/triggers or the search scoring — retrieval
# regressions are invisible until an agent quietly follows the wrong runbook.
#
# Usage: scripts/runbook-retrieval-check.sh
# Requires: panda CLI configured against the server, jq.
set -u

# "query|expected_top1_stem"
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
  "why is finality stalled|ethereum_protocol_model"
  "engine_getBlobs returning empty|blob_propagation_vs_getblobs"
  # intent phrases (mirror runbook names/owns-lines)
  "hosted devnet context intake|hosted_devnet_context"
  "build a kurtosis config from a hosted devnet|kurtosis_devnet_config"
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
  "clickhouse block number range time window|clickhouse_querying"
  "panda compute devnet lifecycle|panda_compute_kurtosis_lifecycle"
  # confusable pairs (the near-miss neighbor must not win)
  "prysm is forked can you investigate|debug_ethereum_network"
  "grandine is forked can you investigate|debug_ethereum_network"
  "watch a running devnet live for a few epochs|devnet_watch"
  "restore a snapshot on remote compute|panda_compute_kurtosis_lifecycle"
  "read logs from a local kurtosis enclave|kurtosis_devnet"
  "clickhouse query slow timing out|clickhouse_querying"
  "make a chart of query results|sandbox_output_conventions"
)

# Out-of-scope probes: nothing in the registry covers these. Informational
# only (absolute score thresholds are corpus-dependent) — they print the top
# hit + score so a human can spot the registry claiming expertise it lacks.
OUT_OF_SCOPE=(
  "write a promql alerting rule"
  "solidity reentrancy vulnerability"
)

pass=0 fail=0

for entry in "${MATRIX[@]}"; do
  q="${entry%%|*}" want="${entry##*|}"
  json=$(panda search runbooks "$q" -o json 2>/dev/null)
  top=$(echo "$json" | jq -r '.results[0].ref // "NONE"' | sed 's|runbooks://||')
  score=$(echo "$json" | jq -r '.results[0].similarity_score // 0' | cut -c1-4)
  if [ "$top" = "$want" ]; then
    echo "PASS  ${top} (${score})  <- \"${q}\""
    pass=$((pass + 1))
  else
    top3=$(echo "$json" | jq -r '[.results[0:3][].ref] | join(", ")')
    echo "FAIL  top=${top} (${score}) want=${want} top3=[${top3}]  <- \"${q}\""
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
