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
# entrypoint, and prints a parseable result line.
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
  if cat "$HOME/.config/panda/credentials/credentials.json" >/dev/null 2>&1; then read=ok; else read=na; fi
  echo "RESULT uid=$(id -u) read=$read"
'
EOF

# case = "<creds-owner> <expected-uid> <expected-read>"
fail=0
for case in \
  "1010:1010 1010 ok" \
  "1000:1000 1000 ok" \
  "0:0 0 ok" \
  "none 1000 na"; do
  # shellcheck disable=SC2086 # intentional split of the space-separated case
  set -- $case
  owner="$1"; want_uid="$2"; want_read="$3"
  out=$(docker run --rm --entrypoint sh "$IMAGE" -c "$PROBE" _ "$owner")
  line=$(printf '%s\n' "$out" | grep '^RESULT ' || true)
  if [ "$line" = "RESULT uid=$want_uid read=$want_read" ]; then
    echo "PASS  creds=$owner -> $line"
  else
    echo "FAIL  creds=$owner -> want 'RESULT uid=$want_uid read=$want_read', got '$line'"
    fail=1
  fi
done

if [ "$fail" = "0" ]; then
  echo "entrypoint UID matrix passed"
else
  echo "entrypoint UID matrix FAILED"
  exit 1
fi
