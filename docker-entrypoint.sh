#!/bin/sh
set -e

# Run the server as whoever owns the mounted credentials directory, so it can
# read and refresh the 0600 OAuth credential files the host wrote there.
#
# The image bakes a `panda` user at UID/GID 1000. When the host user driving
# panda has a different UID — a dedicated service account, a non-1000 primary
# user — the mounted credentials are owned by that UID and `panda` (1000) cannot
# read them: `panda auth status` fails with "permission denied" and every server
# token refresh re-writes the creds owned by 1000, so a host-side chown never
# sticks. Re-number the baked `panda` user to match the credentials owner before
# dropping to it, so the rest of the startup (volume chown, Docker group
# membership, HOME resolution) keeps working unchanged.
#
# Rootless Docker is the exception: the host user maps to container UID 0, so the
# credentials appear root-owned. Run as root directly (under rootless that maps
# back to the unprivileged host user, so it is not a privilege escalation) with
# HOME set so the credential path beneath it still resolves.
CRED_DIR="/home/panda/.config/panda/credentials"
CRED_UID=1000
if [ -d "$CRED_DIR" ]; then
    CRED_UID=$(stat -c '%u' "$CRED_DIR" 2>/dev/null || echo 1000)
fi

if [ "$CRED_UID" = "0" ]; then
    export HOME=/home/panda
    exec "$@"
fi

# Non-1000 host user: re-number the panda user (and its primary group, when that
# GID is free) to own the credentials it reads and rewrites. usermod re-owns the
# files already under /home/panda for us; data dirs are chowned below.
if [ "$CRED_UID" != "1000" ]; then
    CRED_GID=$(stat -c '%g' "$CRED_DIR" 2>/dev/null || echo "$CRED_UID")
    if [ "$CRED_GID" != "1000" ] && ! getent group "$CRED_GID" >/dev/null 2>&1; then
        groupmod -g "$CRED_GID" panda 2>/dev/null || true
    fi
    usermod -u "$CRED_UID" panda 2>/dev/null || true

    # Fail loudly rather than silently drop to UID 1000 and recreate the
    # unreadable-credentials bug — e.g. usermod/groupmod missing, or $CRED_UID
    # already taken by another account.
    if [ "$(id -u panda)" != "$CRED_UID" ]; then
        echo "docker-entrypoint: failed to run as credentials owner UID $CRED_UID" \
             "(panda is still UID $(id -u panda)); credentials would be unreadable." >&2
        exit 1
    fi
fi

PANDA_UID=$(id -u panda)
PANDA_GID=$(id -g panda)

# Fix ownership of mounted volumes that Docker may create as root, plus the
# build-time directories the renumbered panda user no longer owns, so the server
# can write them after dropping privileges.
for dir in /data/storage /data/cache /output /shared; do
    if [ -d "$dir" ] && [ "$(stat -c '%u' "$dir")" != "$PANDA_UID" ]; then
        chown "$PANDA_UID:$PANDA_GID" "$dir"
    fi
done

# If the Docker socket is mounted, add panda to its group so the server
# can manage sandbox containers after dropping root.
# --group-add at the container level is lost by su-exec/gosu, so we
# persist the group in /etc/group instead.
if [ -S /var/run/docker.sock ]; then
    DOCKER_GID=$(stat -c '%g' /var/run/docker.sock)
    if ! getent group "$DOCKER_GID" >/dev/null 2>&1; then
        addgroup -g "$DOCKER_GID" docker-host 2>/dev/null || groupadd -g "$DOCKER_GID" docker-host 2>/dev/null || true
    fi
    DOCKER_GROUP=$(getent group "$DOCKER_GID" | cut -d: -f1)
    addgroup panda "$DOCKER_GROUP" 2>/dev/null || usermod -aG "$DOCKER_GROUP" panda 2>/dev/null || true
fi

# Drop to the panda user. su-exec/gosu set HOME from the passwd entry, but set
# it explicitly too so the credential path beneath it resolves regardless.
# Support both su-exec (Alpine) and gosu (Debian).
export HOME=/home/panda
if command -v su-exec >/dev/null 2>&1; then
    exec su-exec panda "$@"
elif command -v gosu >/dev/null 2>&1; then
    exec gosu panda "$@"
else
    exec "$@"
fi
