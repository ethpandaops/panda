#!/usr/bin/env bash
# Reject dumping-ground names: code lives in files named after a domain,
# never in helpers/utils grab-bags. See CLAUDE.md architectural guardrails.
set -euo pipefail

cd "$(dirname "$0")/.."

fail=0

if matches=$(git ls-files '*.go' | grep -E '(^|/|_)(helpers?|utils?|common|misc)\.go$'); then
  echo "generic file names are not allowed — name files after their domain:"
  echo "$matches" | sed 's/^/  /'
  fail=1
fi

if matches=$(git grep -nE '^package (helpers?|utils?|common|misc)$' -- '*.go'); then
  echo "generic package names are not allowed:"
  echo "$matches" | sed 's/^/  /'
  fail=1
fi

exit "$fail"
