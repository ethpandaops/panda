#!/usr/bin/env bash
#
# Runs govulncheck and fails only on *reachable* vulnerabilities that are not
# explicitly allowlisted. govulncheck has no native allowlist, so we scan in
# JSON mode and gate on the result ourselves.
#
# The allowlist holds advisories that have been assessed and accepted because
# no fixed version is available on an import path we can use. Remove an entry
# the moment a usable fix ships — Dependabot will bump the dependency and this
# script will then fail if the entry is left behind, which is the intended nudge.
set -euo pipefail

# GO-2026-4887 (CVE-2026-34040) and GO-2026-4883 (CVE-2026-33997): moby daemon
# AuthZ-plugin CVEs. The fix exists only on the new github.com/moby/moby/v2
# module path (2.0.0-beta.8, a beta); the github.com/docker/docker path we
# import (also pulled transitively by testcontainers-go) has no fixed release
# above v28.5.2. Both are daemon-side, exploitable only on a daemon running
# authorization plugins — panda is a client of the daemon, not a host for it.
# Drop these once a stable fix lands on a usable import path.
ALLOWLIST=(
  GO-2026-4887
  GO-2026-4883
)

# Human-readable report for the logs. govulncheck exits non-zero when it finds
# affecting vulnerabilities, so don't let that abort the script here — the gate
# below is what decides pass/fail.
govulncheck ./... || true
echo "----------------------------------------"

json="$(govulncheck -format json ./...)"

# An advisory is "reachable" when at least one finding's most specific trace
# frame names a called function (as opposed to a merely-imported module).
mapfile -t reachable < <(
  printf '%s' "$json" \
    | jq -r 'select(.finding != null and .finding.trace[0].function != null) | .finding.osv' \
    | sort -u
)

if [ "${#reachable[@]}" -eq 0 ]; then
  echo "govulncheck gate: no reachable vulnerabilities."
  exit 0
fi

echo "govulncheck gate: reachable advisories: ${reachable[*]}"

unexpected=()
for id in "${reachable[@]}"; do
  allowed=
  for a in "${ALLOWLIST[@]}"; do
    [ "$id" = "$a" ] && allowed=1 && break
  done
  [ -n "$allowed" ] || unexpected+=("$id")
done

if [ "${#unexpected[@]}" -gt 0 ]; then
  echo "::error::reachable vulnerabilities not in the allowlist: ${unexpected[*]}"
  echo "See the report above for call traces. Fix the vulnerability, or — only if"
  echo "it is genuinely unfixable and assessed — add it to ALLOWLIST in"
  echo "scripts/govulncheck.sh with a justification."
  exit 1
fi

echo "govulncheck gate: all ${#reachable[@]} reachable advisory(ies) are allowlisted (assessed, no usable fix)."
