#!/bin/sh
set -e

# Fix ownership of mounted volumes that Docker may create as root.
# The panda user (UID 1000) needs write access to the storage directory.
if [ -d /data/storage ] && [ "$(stat -c '%u' /data/storage)" != "1000" ]; then
    chown panda:panda /data/storage
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
