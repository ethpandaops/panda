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

# Drop to the panda user and exec the requested command.
# Support both su-exec (Alpine) and gosu (Debian).
if command -v su-exec >/dev/null 2>&1; then
    exec su-exec panda "$@"
elif command -v gosu >/dev/null 2>&1; then
    exec gosu panda "$@"
else
    exec "$@"
fi
