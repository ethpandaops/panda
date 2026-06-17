#!/bin/sh
set -e

# Fix ownership of mounted volumes that Docker may create as root.
# The panda user (UID 1000) needs write access to the data directories.
for dir in /data/storage /data/cache; do
    if [ -d "$dir" ] && [ "$(stat -c '%u' "$dir")" != "1000" ]; then
        chown panda:panda "$dir"
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

# Decide which user to run the server as.
#
# By default we drop to the unprivileged `panda` user (UID 1000). That works
# under rootful Docker, where the host user who ran `panda auth login` typically
# also has UID 1000, so the mounted 0600 credential files are owned by `panda`
# inside the container and stay readable after the drop.
#
# Under rootless Docker the host user maps to container UID 0, so host-written
# files (the mounted credentials) appear owned by root inside the container and
# the `panda` user can neither read nor refresh them — proxy auth then fails
# with "permission denied" and `panda datasources` comes back empty. When the
# mounted credentials are root-owned, run as root instead: under rootless that
# maps back to the unprivileged host user (so it is not a privilege escalation),
# and HOME is set so the credential path beneath it still resolves.
CRED_DIR="/home/panda/.config/panda/credentials"
if [ -d "$CRED_DIR" ] && [ "$(stat -c '%u' "$CRED_DIR" 2>/dev/null)" = "0" ]; then
    export HOME=/home/panda
    exec "$@"
fi

# Rootful: drop to the panda user.
# Support both su-exec (Alpine) and gosu (Debian).
if command -v su-exec >/dev/null 2>&1; then
    exec su-exec panda "$@"
elif command -v gosu >/dev/null 2>&1; then
    exec gosu panda "$@"
else
    exec "$@"
fi
