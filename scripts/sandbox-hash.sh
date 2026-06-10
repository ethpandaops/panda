#!/usr/bin/env bash
# Deterministic content hash of everything baked into the sandbox image.
#
# CI tags the sandbox image with this hash (sandbox-<hash>) and skips the (slow)
# rebuild when an image for the current hash already exists in the registry. The
# hash covers exactly the build inputs the Dockerfile COPYs in, so it changes iff
# the sandbox actually needs rebuilding:
#   - sandbox/Dockerfile
#   - sandbox/requirements.in / requirements.txt (intent + compiled hash lock)
#   - sandbox/ethpandaops/**            (the ethpandaops runtime package)
#   - modules/*/python/*.py             (per-module Python injected into the package)
set -euo pipefail

cd "$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"

_sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum
  else
    shasum -a 256
  fi
}

{
  echo sandbox/Dockerfile
  echo sandbox/requirements.in
  echo sandbox/requirements.txt
  find sandbox/ethpandaops -type f
  find modules -type f -path '*/python/*.py'
} | sort -u | while IFS= read -r f; do
  printf '%s:%s\n' "$f" "$(_sha256 < "$f" | awk '{print $1}')"
done | _sha256 | awk '{print substr($1, 1, 16)}'
