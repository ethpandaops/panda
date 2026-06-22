#!/usr/bin/env bash
#
# Verify docker-entrypoint.sh runs the server as the owner of the mounted
# credentials directory, so non-1000 host users (service accounts) can read and
# refresh their OAuth credentials. Runs the image through a matrix of credential
# owners, each in a fresh container.
#
# Usage: scripts/test-entrypoint-uid.sh <image>
set -euo pipefail

IMAGE="${1:?usage: test-entrypoint-uid.sh <image>}"

# Probe script run *inside* a fresh container as root. It seeds a credentials
# directory owned by $1 (or omits it for the pre-login case), invokes the real
# entrypoint, then — as the dropped user — reads the credential and rewrites it
# the way the server's token refresh does (temp file + chmod 0600 + rename),
# asserting the rewritten file lands owned by the running UID. That ownership is
# exactly what keeps the host user able to read the credential after a refresh.
read -r -d '' PROBE <<'EOF' || true
set -e
owner="$1"
rm -rf /home/panda/.config
if [ "$owner" != "none" ]; then
  mkdir -p /home/panda/.config/panda/credentials
  echo '{"t":1}' > /home/panda/.config/panda/credentials/credentials.json
  chmod 0600 /home/panda/.config/panda/credentials/credentials.json
  chown -R "$owner" /home/panda/.config/panda/credentials
  # Intermediate dirs are root-owned 0755 the way a Docker bind mount creates them.
  chown root:root /home/panda/.config /home/panda/.config/panda
fi
/usr/local/bin/docker-entrypoint.sh sh -c '
  d="$HOME/.config/panda/credentials"
  f="$d/credentials.json"
  if [ -f "$f" ] && cat "$f" >/dev/null 2>&1; then read=ok; else read=na; fi
  # Mirror the server refresh write: MkdirAll 0700, temp file, 0600, rename.
  mkdir -p "$d" && chmod 0700 "$d"
  tmp=$(mktemp "$d/.cred-XXXXXX")
  echo "{\"t\":2}" > "$tmp" && chmod 0600 "$tmp" && mv "$tmp" "$f"
  echo "RESULT uid=$(id -u) read=$read wrote=$(stat -c %u "$f")"
'
EOF

# case = "<creds-owner> <expected-uid> <expected-read> <expected-write-owner>"
# - 1010: a non-1000 service account (issue #240) -> server runs as 1010.
# - 1000: the rootful default.
# - 0:    rootless Docker (creds appear root-owned) -> server runs as root.
# - none: pre-login, no credentials mounted yet -> default panda 1000.
fail=0
for case in \
  "1010:1010 1010 ok 1010" \
  "1000:1000 1000 ok 1000" \
  "0:0 0 ok 0" \
  "none 1000 na 1000"; do
  # shellcheck disable=SC2086 # intentional split of the space-separated case
  set -- $case
  owner="$1"; want="RESULT uid=$2 read=$3 wrote=$4"
  out=$(docker run --rm --entrypoint sh "$IMAGE" -c "$PROBE" _ "$owner")
  line=$(printf '%s\n' "$out" | grep '^RESULT ' || true)
  if [ "$line" = "$want" ]; then
    echo "PASS  creds=$owner -> $line"
  else
    echo "FAIL  creds=$owner -> want '$want', got '$line'"
    fail=1
  fi
done

# A credentials owner that collides with an existing account must fail loudly,
# not silently drop to UID 1000 and recreate the unreadable-credentials bug.
# UID 1 (bin) always exists, so re-numbering panda to it cannot succeed.
neg=$(docker run --rm --entrypoint sh "$IMAGE" -c '
  mkdir -p /home/panda/.config/panda/credentials
  chown -R 1:1 /home/panda/.config/panda/credentials
  /usr/local/bin/docker-entrypoint.sh true >/dev/null 2>&1 && echo EXIT0 || echo EXITNZ
') || true
if [ "$neg" = "EXITNZ" ]; then
  echo "PASS  creds=1:1 (collision) -> entrypoint exited non-zero"
else
  echo "FAIL  creds=1:1 (collision) -> expected non-zero exit, got '$neg'"
  fail=1
fi

if [ "$fail" = "0" ]; then
  echo "entrypoint UID matrix passed"
else
  echo "entrypoint UID matrix FAILED"
  exit 1
fi
